package netflix

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// detail is the account information scraped out of /account.
type detail struct {
	Email         string
	Plan          string
	Quality       string
	Screens       string
	Price         string
	NextBilling   string
	MemberSince   string
	Country       string
	ProfileName   string
	Profiles      int
	Status        string
	Payment       string
	Phone         string
	EmailVerified string
	PhoneVerified string
	OnHold        string
	ExtraMember   string
}

// asMeta renders populated fields for the report.
func (d detail) asMeta() map[string]string {
	m := map[string]string{}
	put := func(k, v string) {
		v = strings.TrimSpace(v)
		if v != "" && v != "N/A" && v != "null" {
			m[k] = v
		}
	}
	put("Plan", d.Plan)
	put("Quality", d.Quality)
	put("Screens", d.Screens)
	put("Price", d.Price)
	put("Next billing", d.NextBilling)
	put("Member since", d.MemberSince)
	put("Country", d.Country)
	put("Status", d.Status)
	put("Payment", d.Payment)
	put("Phone", d.Phone)
	put("Email verified", d.EmailVerified)
	put("Phone verified", d.PhoneVerified)
	put("On hold", d.OnHold)
	put("Extra member", d.ExtraMember)
	if d.Profiles > 0 {
		m["Profiles"] = strconv.Itoa(d.Profiles)
	}
	put("Profile", d.ProfileName)
	return m
}

var (
	reEmail       = regexp.MustCompile(`"emailAddress"\s*:\s*"([^"]+)"`)
	reEmailAlt    = regexp.MustCompile(`"email"\s*:\s*"([^"]+)"`)
	rePlan        = regexp.MustCompile(`"localizedPlanName"[^}]*?"value"\s*:\s*"([^"]+)"`)
	rePlanAlt     = regexp.MustCompile(`"planName"\s*:\s*"([^"]+)"`)
	reQuality     = regexp.MustCompile(`"videoQuality"[^}]*?"value"\s*:\s*"([^"]+)"`)
	reScreens     = regexp.MustCompile(`"maxStreams"[^}]*?"value"\s*:\s*(\d+)`)
	rePrice       = regexp.MustCompile(`"planPrice"[^}]*?"value"\s*:\s*"([^"]+)"`)
	reNextBill    = regexp.MustCompile(`"nextBillingDate"[^}]*?"value"\s*:\s*"([^"]+)"`)
	reMemberSince = regexp.MustCompile(`"memberSince"\s*:\s*(\d{10,})`)
	reCountry     = regexp.MustCompile(`"currentCountry"\s*:\s*"([^"]+)"`)
	reCountryAlt  = regexp.MustCompile(`"countryOfSignup"\s*:\s*"([^"]+)"`)
	reStatus      = regexp.MustCompile(`"membershipStatus"\s*:\s*"([^"]+)"`)
	rePayment     = regexp.MustCompile(`"paymentMethodName"\s*:\s*"([^"]+)"`)
	reCardBrand   = regexp.MustCompile(`"(?:cardBrand|cardType)"\s*:\s*"([^"]+)"`)
	reLast4       = regexp.MustCompile(`"(?:last4Digits|lastFourDigits)"\s*:\s*"(\d{2,4})"`)
	rePhone       = regexp.MustCompile(`"formattedPhoneNumber"\s*:\s*"([^"]+)"`)
	reProfileName = regexp.MustCompile(`"(?:profileName|displayName)"\s*:\s*"([^"]+)"`)
	reOnHold      = regexp.MustCompile(`"(?:isUserOnHold|onHold)"\s*:\s*(true|false)`)
	reEmailVer    = regexp.MustCompile(`"isEmailVerified"\s*:\s*(true|false)`)
	rePhoneVer    = regexp.MustCompile(`"isPhoneVerified"\s*:\s*(true|false)`)
	reExtraMember = regexp.MustCompile(`"type"\s*:\s*"EXTRA_MEMBER"`)
)

// parseAccount pulls everything it can out of the /account page. It tries the
// structured reactContext first and falls back to targeted regexes, which is
// what covers Netflix's newer Apollo-rendered markup.
func parseAccount(body string) detail {
	var d detail

	ctx := reactContext(body)
	if ctx != nil {
		applyContext(&d, ctx)
	}

	if d.Email == "" {
		d.Email = firstMatch(body, reEmail, reEmailAlt)
	}
	if d.Plan == "" {
		d.Plan = firstMatch(body, rePlan, rePlanAlt)
	}
	if d.Quality == "" {
		d.Quality = firstMatch(body, reQuality)
	}
	if d.Screens == "" {
		d.Screens = firstMatch(body, reScreens)
	}
	if d.Price == "" {
		d.Price = firstMatch(body, rePrice)
	}
	if d.NextBilling == "" {
		d.NextBilling = firstMatch(body, reNextBill)
	}
	if d.Country == "" {
		d.Country = firstMatch(body, reCountry, reCountryAlt)
	}
	if d.Status == "" {
		d.Status = strings.ToUpper(firstMatch(body, reStatus))
	}
	if d.Phone == "" {
		d.Phone = firstMatch(body, rePhone)
	}
	if d.EmailVerified == "" {
		d.EmailVerified = boolWord(firstMatch(body, reEmailVer))
	}
	if d.PhoneVerified == "" {
		d.PhoneVerified = boolWord(firstMatch(body, rePhoneVer))
	}
	if d.OnHold == "" {
		d.OnHold = boolWord(firstMatch(body, reOnHold))
	}
	if d.ExtraMember == "" && reExtraMember.MatchString(body) {
		d.ExtraMember = "Yes"
	}

	if d.MemberSince == "" {
		if ms := firstMatch(body, reMemberSince); ms != "" {
			d.MemberSince = epochToDate(ms)
		}
	}

	if d.Payment == "" {
		brand := firstMatch(body, reCardBrand)
		last4 := firstMatch(body, reLast4)
		switch {
		case brand != "" && last4 != "":
			d.Payment = brand + " *" + last4
		case last4 != "":
			d.Payment = "*" + last4
		default:
			d.Payment = firstMatch(body, rePayment)
		}
	}

	names := uniqueProfileNames(body)
	if len(names) > 0 {
		d.ProfileName = strings.Join(names, " | ")
		d.Profiles = len(names)
	}

	d.Email = unescape(d.Email)
	d.Plan = unescape(d.Plan)
	d.Quality = unescape(d.Quality)
	d.Price = unescape(d.Price)
	d.Country = unescape(d.Country)

	return d
}

func applyContext(d *detail, ctx map[string]any) {
	models := asMap(ctx["models"])

	user := asMap(asMap(models["user"])["data"])
	if len(user) == 0 {
		user = asMap(asMap(models["memberContext"])["data"])
	}

	if v := asString(user["email"]); v != "" {
		d.Email = v
	}
	if d.Email == "" {
		if ea := asMap(user["emailAddress"]); ea != nil {
			d.Email = asString(ea["value"])
		}
	}
	if v := asString(user["currentCountry"]); v != "" {
		d.Country = v
	}
	if v := asString(user["membershipStatus"]); v != "" {
		d.Status = strings.ToUpper(v)
	}
	if v := asString(user["name"]); v != "" {
		d.ProfileName = v
	}
	if b, ok := user["isEmailVerified"].(bool); ok {
		d.EmailVerified = yesNo(b)
	}
	if b, ok := user["isPhoneVerified"].(bool); ok {
		d.PhoneVerified = yesNo(b)
	}
	if b, ok := user["isUserOnHold"].(bool); ok {
		d.OnHold = yesNo(b)
	}

	bill := asMap(asMap(asMap(models["membershipData"])["data"])["membership"])
	if len(bill) == 0 {
		bill = asMap(asMap(models["billingSummary"])["data"])
	}
	if v := nestedValue(bill["localizedPlanName"]); v != "" {
		d.Plan = v
	}
	if v := nestedValue(bill["videoQuality"]); v != "" {
		d.Quality = v
	}
	if v := nestedValue(bill["maxStreams"]); v != "" {
		d.Screens = v
	}
	if v := nestedValue(bill["planPrice"]); v != "" {
		d.Price = v
	}
	if v := nestedValue(bill["nextBillingDate"]); v != "" {
		d.NextBilling = v
	}
	if v := nestedValue(bill["memberSince"]); v != "" {
		d.MemberSince = epochToDate(v)
	}

	if profiles, ok := asMap(models["profilesList"])["data"].([]any); ok {
		d.Profiles = len(profiles)
	}
}

// reactContext pulls the first balanced JSON object after a Netflix context
// marker. A lazy regex cannot do this — the object nests.
func reactContext(src string) map[string]any {
	for _, marker := range []string{
		"window.netflix.reactContext",
		"window.netflix.initialContext",
	} {
		idx := strings.Index(src, marker)
		if idx < 0 {
			continue
		}
		start := strings.IndexByte(src[idx:], '{')
		if start < 0 {
			continue
		}
		start += idx
		if end := balancedEnd(src, start); end > start {
			var m map[string]any
			if err := json.Unmarshal([]byte(src[start:end]), &m); err == nil {
				return m
			}
		}
	}
	return nil
}

func balancedEnd(s string, start int) int {
	depth := 0
	inStr := false
	esc := false

	for i := start; i < len(s); i++ {
		ch := s[i]
		if inStr {
			switch {
			case esc:
				esc = false
			case ch == '\\':
				esc = true
			case ch == '"':
				inStr = false
			}
			continue
		}
		switch ch {
		case '"':
			inStr = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i + 1
			}
		}
	}
	return -1
}

func uniqueProfileNames(body string) []string {
	seen := map[string]struct{}{}
	var out []string

	for _, m := range reProfileName.FindAllStringSubmatch(body, -1) {
		name := strings.TrimSpace(unescape(m[1]))
		if name == "" || len(name) > 40 {
			continue
		}
		if strings.ContainsAny(name, "\\{}<>") {
			continue
		}
		lower := strings.ToLower(name)
		switch lower {
		case "english", "india", "indonesia", "español (estados unidos)":
			continue
		}
		if _, dup := seen[lower]; dup {
			continue
		}
		seen[lower] = struct{}{}
		out = append(out, name)
		if len(out) >= 8 {
			break
		}
	}
	return out
}

func firstMatch(body string, patterns ...*regexp.Regexp) string {
	for _, re := range patterns {
		if m := re.FindStringSubmatch(body); len(m) > 1 {
			v := strings.TrimSpace(m[1])
			if v != "" && v != "null" {
				return v
			}
		}
	}
	return ""
}

func boolWord(s string) string {
	switch strings.ToLower(s) {
	case "true":
		return "Yes"
	case "false":
		return "No"
	}
	return ""
}

func yesNo(b bool) string {
	if b {
		return "Yes"
	}
	return "No"
}

func epochToDate(s string) string {
	ms, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil || ms <= 0 {
		return ""
	}
	if ms > 1e12 {
		ms /= 1000
	}
	return time.Unix(ms, 0).UTC().Format("2006-01-02")
}

func unescape(s string) string {
	replacer := strings.NewReplacer(
		`\x20`, " ",
		`\x40`, "@",
		`\u0026`, "&",
		`\u003c`, "<",
		`\u003e`, ">",
		`\/`, "/",
		`\"`, `"`,
	)
	return strings.TrimSpace(replacer.Replace(s))
}

func asMap(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return nil
}

func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// nestedValue reads both `"x":{"value":"y"}` and `"x":{"value":123}`.
func nestedValue(v any) string {
	m := asMap(v)
	if m == nil {
		if s, ok := v.(string); ok {
			return s
		}
		return ""
	}
	switch val := m["value"].(type) {
	case string:
		return val
	case float64:
		return strconv.FormatInt(int64(val), 10)
	}
	return ""
}
