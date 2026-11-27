package netflix

import (
	"strings"
	"testing"
)

func TestDetect(t *testing.T) {
	if !Detect("NetflixId=abc; SecureNetflixId=def") {
		t.Error("should detect NetflixId")
	}
	if Detect("MSPAuth=abc; MSPProf=def") {
		t.Error("should not detect Microsoft cookies")
	}
}

func TestParseRaw(t *testing.T) {
	cookies, issues := ParseCookies("NetflixId=NF1; SecureNetflixId=SNF1; nfvdid=VD")
	if len(issues) != 0 {
		t.Fatalf("issues: %v", issues)
	}
	if len(cookies) != 1 {
		t.Fatalf("got %d cookies, want 1", len(cookies))
	}
	if cookies[0].Values["NetflixId"] != "NF1" {
		t.Errorf("NetflixId = %q", cookies[0].Values["NetflixId"])
	}
}

func TestParseMissingID(t *testing.T) {
	cookies, issues := ParseCookies("SecureNetflixId=SNF1")
	if len(cookies) != 0 {
		t.Fatalf("got %d cookies, want 0", len(cookies))
	}
	if len(issues) == 0 {
		t.Fatal("expected an issue")
	}
}

func TestParseJSONArray(t *testing.T) {
	raw := `[{"name":"NetflixId","value":"NF1","domain":".netflix.com"},{"name":"SecureNetflixId","value":"SNF1","domain":".netflix.com"}]`
	cookies, issues := ParseCookies(raw)
	if len(issues) != 0 {
		t.Fatalf("issues: %v", issues)
	}
	if cookies[0].Format != "json" {
		t.Errorf("format = %q", cookies[0].Format)
	}
	if cookies[0].Values["NetflixId"] != "NF1" {
		t.Errorf("NetflixId = %q", cookies[0].Values["NetflixId"])
	}
}

func TestParseNetscape(t *testing.T) {
	raw := "# Netscape HTTP Cookie File\n" +
		".netflix.com\tTRUE\t/\tTRUE\t1900000000\tNetflixId\tNF1\n" +
		".netflix.com\tTRUE\t/\tTRUE\t1900000000\tSecureNetflixId\tSNF1\n"
	cookies, issues := ParseCookies(raw)
	if len(issues) != 0 {
		t.Fatalf("issues: %v", issues)
	}
	if cookies[0].Format != "netscape" {
		t.Errorf("format = %q", cookies[0].Format)
	}
	if cookies[0].Values["SecureNetflixId"] != "SNF1" {
		t.Errorf("SecureNetflixId = %q", cookies[0].Values["SecureNetflixId"])
	}
}

func TestParseStackedLine(t *testing.T) {
	raw := "NetflixId=AAA; SecureNetflixId=BBB; NetflixId=CCC; SecureNetflixId=DDD"
	cookies, issues := ParseCookies(raw)
	if len(issues) != 0 {
		t.Fatalf("issues: %v", issues)
	}
	if len(cookies) != 2 {
		t.Fatalf("got %d cookies, want 2", len(cookies))
	}
	if cookies[0].Values["NetflixId"] != "AAA" || cookies[1].Values["NetflixId"] != "CCC" {
		t.Errorf("split wrong: %v / %v",
			cookies[0].Values["NetflixId"], cookies[1].Values["NetflixId"])
	}
}

func TestParseAccountFromReactContext(t *testing.T) {
	body := `<html><script>
window.netflix.reactContext = {"models":{
 "user":{"data":{"email":"jane@example.com","currentCountry":"US",
   "membershipStatus":"CURRENT_MEMBER","isEmailVerified":true}},
 "membershipData":{"data":{"membership":{
   "localizedPlanName":{"value":"Premium"},
   "videoQuality":{"value":"4K+HDR"},
   "maxStreams":{"value":4},
   "planPrice":{"value":"$22.99"},
   "nextBillingDate":{"value":"2026-12-01"},
   "memberSince":{"value":1700000000000}}}},
 "profilesList":{"data":[{"name":"Jane"},{"name":"Kid"}]}
}};
</script></html>`

	d := parseAccount(body)

	if d.Email != "jane@example.com" {
		t.Errorf("email = %q", d.Email)
	}
	if d.Plan != "Premium" {
		t.Errorf("plan = %q", d.Plan)
	}
	if d.Quality != "4K+HDR" {
		t.Errorf("quality = %q", d.Quality)
	}
	if d.Screens != "4" {
		t.Errorf("screens = %q", d.Screens)
	}
	if d.Country != "US" {
		t.Errorf("country = %q", d.Country)
	}
	if d.Profiles != 2 {
		t.Errorf("profiles = %d", d.Profiles)
	}
	if d.MemberSince != "2023-11-14" {
		t.Errorf("member since = %q", d.MemberSince)
	}
	if d.EmailVerified != "Yes" {
		t.Errorf("email verified = %q", d.EmailVerified)
	}
}

func TestParseAccountRegexFallback(t *testing.T) {
	body := `{"emailAddress":"bob@example.com","localizedPlanName":{"value":"Standard"},
	  "videoQuality":{"value":"1080p"},"maxStreams":{"value":2},
	  "currentCountry":"GB","membershipStatus":"CURRENT_MEMBER"}`

	d := parseAccount(body)

	if d.Email != "bob@example.com" {
		t.Errorf("email = %q", d.Email)
	}
	if d.Plan != "Standard" {
		t.Errorf("plan = %q", d.Plan)
	}
	if d.Screens != "2" {
		t.Errorf("screens = %q", d.Screens)
	}
	if d.Country != "GB" {
		t.Errorf("country = %q", d.Country)
	}
	if !strings.EqualFold(d.Status, "CURRENT_MEMBER") {
		t.Errorf("status = %q", d.Status)
	}
}

func TestAsMetaSkipsEmpty(t *testing.T) {
	d := detail{Plan: "Premium", Country: "N/A"}
	m := d.asMeta()
	if m["Plan"] != "Premium" {
		t.Errorf("plan meta = %q", m["Plan"])
	}
	if _, ok := m["Country"]; ok {
		t.Errorf("N/A should be omitted, got %q", m["Country"])
	}
}
