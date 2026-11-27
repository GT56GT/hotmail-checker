package instagram

import (
	"testing"

	fhttp "github.com/bogdanfinn/fhttp"

	"github.com/GT56GT/hotmail-checker/internal/provider"
)

func TestDetect(t *testing.T) {
	if !Detect("sessionid=abc; ds_user_id=123") {
		t.Error("should detect sessionid")
	}
	if Detect("MSPAuth=abc; MSPProf=def") {
		t.Error("should not detect Microsoft cookies")
	}
}

func TestParseRaw(t *testing.T) {
	cookies, issues := ParseCookies("sessionid=SID; ds_user_id=42; csrftoken=CSRF; mid=MID")
	if len(issues) != 0 {
		t.Fatalf("issues: %v", issues)
	}
	if len(cookies) != 1 {
		t.Fatalf("got %d cookies, want 1", len(cookies))
	}
	if cookies[0].Values["sessionid"] != "SID" {
		t.Errorf("sessionid = %q", cookies[0].Values["sessionid"])
	}
	if SessionID(cookies[0]) != "SID" {
		t.Errorf("SessionID() = %q", SessionID(cookies[0]))
	}
}

func TestParseMissingSession(t *testing.T) {
	cookies, issues := ParseCookies("ds_user_id=42; csrftoken=CSRF")
	if len(cookies) != 0 {
		t.Fatalf("got %d cookies, want 0", len(cookies))
	}
	if len(issues) == 0 {
		t.Fatal("expected an issue")
	}
}

func TestParseJSONArray(t *testing.T) {
	raw := `[{"name":"sessionid","value":"SID","domain":".instagram.com"},` +
		`{"name":"ds_user_id","value":"42","domain":".instagram.com"},` +
		`{"name":"evil","value":"x","domain":".example.com"}]`
	cookies, issues := ParseCookies(raw)
	if len(issues) != 0 {
		t.Fatalf("issues: %v", issues)
	}
	if cookies[0].Format != "json" {
		t.Errorf("format = %q", cookies[0].Format)
	}
	if _, ok := cookies[0].Values["evil"]; ok {
		t.Error("foreign-domain cookie should be dropped")
	}
}

func TestParseNetscape(t *testing.T) {
	raw := "# Netscape HTTP Cookie File\n" +
		".instagram.com\tTRUE\t/\tTRUE\t1900000000\tsessionid\tSID\n" +
		".instagram.com\tTRUE\t/\tTRUE\t1900000000\tds_user_id\t42\n"
	cookies, issues := ParseCookies(raw)
	if len(issues) != 0 {
		t.Fatalf("issues: %v", issues)
	}
	if cookies[0].Format != "netscape" {
		t.Errorf("format = %q", cookies[0].Format)
	}
	if cookies[0].Values["ds_user_id"] != "42" {
		t.Errorf("ds_user_id = %q", cookies[0].Values["ds_user_id"])
	}
}

func TestParseFormData(t *testing.T) {
	body := []byte(`{"status":"ok","form_data":{` +
		`"username":"jane_doe","full_name":"Jane Doe","email":"jane@example.com",` +
		`"phone_number":"+15551234567","biography":"hello","external_url":"https://x.test"}}`)

	d, ok := parseFormData(body)
	if !ok {
		t.Fatal("parseFormData failed")
	}
	if d.Username != "jane_doe" {
		t.Errorf("username = %q", d.Username)
	}
	if d.Email != "jane@example.com" {
		t.Errorf("email = %q", d.Email)
	}
	if d.Phone != "+15551234567" {
		t.Errorf("phone = %q", d.Phone)
	}
}

func TestParseFormDataRejectsEmpty(t *testing.T) {
	if _, ok := parseFormData([]byte(`{"status":"ok"}`)); ok {
		t.Error("expected rejection")
	}
}

func TestParseCurrentUser(t *testing.T) {
	body := []byte(`{"status":"ok","user":{"pk":12345,"username":"bob",` +
		`"full_name":"Bob B","email":"bob@example.com","is_verified":true,` +
		`"is_business":false,"is_private":true}}`)

	d, ok := parseCurrentUser(body)
	if !ok {
		t.Fatal("parseCurrentUser failed")
	}
	if d.UserID != "12345" {
		t.Errorf("user id = %q", d.UserID)
	}
	if d.Verified != "Yes" {
		t.Errorf("verified = %q", d.Verified)
	}
	if d.Private != "Yes" {
		t.Errorf("private = %q", d.Private)
	}
}

func TestMergeProfile(t *testing.T) {
	body := []byte(`{"data":{"user":{"username":"bob","full_name":"Bob B",` +
		`"is_verified":false,"is_business_account":true,"is_private":false,` +
		`"category_name":"Creator",` +
		`"edge_followed_by":{"count":1500},` +
		`"edge_follow":{"count":120},` +
		`"edge_owner_to_timeline_media":{"count":64}}}}`)

	d := detail{Username: "bob"}
	mergeProfile(&d, body)

	if d.Followers != "1500" {
		t.Errorf("followers = %q", d.Followers)
	}
	if d.Following != "120" {
		t.Errorf("following = %q", d.Following)
	}
	if d.Posts != "64" {
		t.Errorf("posts = %q", d.Posts)
	}
	if d.Business != "Yes" {
		t.Errorf("business = %q", d.Business)
	}
	if d.Category != "Creator" {
		t.Errorf("category = %q", d.Category)
	}
}

func TestRequiresLogin(t *testing.T) {
	yes := []string{
		`{"message":"Please wait","require_login":true,"status":"fail"}`,
		`{"message":"login_required","status":"fail"}`,
	}
	for _, body := range yes {
		if !requiresLogin([]byte(body)) {
			t.Errorf("should detect login requirement: %s", body)
		}
	}

	no := `{"status":"ok","form_data":{"username":"x"}}`
	if requiresLogin([]byte(no)) {
		t.Error("false positive on success payload")
	}
}

func TestIsCheckpoint(t *testing.T) {
	if !isCheckpoint([]byte(`{"message":"checkpoint_required"}`)) {
		t.Error("should detect checkpoint")
	}
	if isCheckpoint([]byte(`{"status":"ok"}`)) {
		t.Error("false positive on success payload")
	}
}

func TestAsMetaSkipsEmpty(t *testing.T) {
	d := detail{Username: "bob", Email: ""}
	m := d.asMeta()
	if m["Username"] != "bob" {
		t.Errorf("username meta = %q", m["Username"])
	}
	if _, ok := m["Email"]; ok {
		t.Error("empty email should be omitted")
	}
}

func TestJudge(t *testing.T) {
	login := &fhttp.Response{
		StatusCode: 302,
		Header:     fhttp.Header{"Location": {"https://www.instagram.com/accounts/login/?next=/api/v1/x"}},
	}
	if v, reason, final := judge(login, nil); !final || v != provider.Dead {
		t.Errorf("login redirect: v=%v reason=%q final=%v", v, reason, final)
	}

	elsewhere := &fhttp.Response{
		StatusCode: 302,
		Header:     fhttp.Header{"Location": {"https://www.instagram.com/"}},
	}
	if v, _, final := judge(elsewhere, nil); !final || v != provider.Dead {
		t.Errorf("unexpected redirect: v=%v final=%v", v, final)
	}

	if v, _, _ := judge(&fhttp.Response{StatusCode: 401}, nil); v != provider.Dead {
		t.Errorf("401 = %v, want dead", v)
	}

	blocked := &fhttp.Response{StatusCode: 403}
	if v, _, _ := judge(blocked, []byte(`{"message":"something went wrong"}`)); v != provider.Failed {
		t.Errorf("403 without marker = %v, want failed", v)
	}
	if v, _, _ := judge(blocked, []byte(`{"require_login":true}`)); v != provider.Dead {
		t.Errorf("403 with require_login = %v, want dead", v)
	}

	if v, _, _ := judge(&fhttp.Response{StatusCode: 429}, nil); v != provider.RateLimited {
		t.Errorf("429 = %v, want rate-limited", v)
	}

	if v, _, _ := judge(&fhttp.Response{StatusCode: 200}, []byte(`{"message":"checkpoint_required"}`)); v != provider.Challenged {
		t.Errorf("checkpoint = %v, want challenged", v)
	}

	for _, code := range []int{400, 404, 500, 503} {
		if _, _, final := judge(&fhttp.Response{StatusCode: code}, nil); final {
			t.Errorf("%d should be inconclusive so the fallback runs", code)
		}
	}
}
