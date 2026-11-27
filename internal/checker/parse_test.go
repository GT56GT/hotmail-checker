package checker

import "testing"

const rawCookie = "Cookie: MSPAuth=ABC123; MSPProf=DEF456; ANON=GHI789; NAP=1; SUBP=2"

func TestParseRaw(t *testing.T) {
	cookies, issues := ParseBlock(rawCookie)
	if len(issues) != 0 {
		t.Fatalf("unexpected issues: %v", issues)
	}
	if len(cookies) != 1 {
		t.Fatalf("want 1 cookie, got %d", len(cookies))
	}
	c := cookies[0]
	if c.Format != "raw" {
		t.Errorf("format = %q, want raw", c.Format)
	}
	if got := c.Values["MSPAuth"]; got != "ABC123" {
		t.Errorf("MSPAuth = %q", got)
	}
}

func TestParseMultiLine(t *testing.T) {
	input := "MSPAuth=A; MSPProf=B; ANON=C\nMSPAuth=D; MSPProf=E; ANON=F\n"
	cookies, issues := ParseBlock(input)
	if len(issues) != 0 {
		t.Fatalf("unexpected issues: %v", issues)
	}
	if len(cookies) != 2 {
		t.Fatalf("want 2 cookies, got %d", len(cookies))
	}
	if cookies[0].Values["MSPAuth"] != "A" || cookies[1].Values["MSPAuth"] != "D" {
		t.Errorf("cookies parsed out of order")
	}
}

func TestParseJSONMap(t *testing.T) {
	input := `{"MSPAuth":"A","MSPProf":"B","ANON":"C"}`
	cookies, issues := ParseBlock(input)
	if len(issues) != 0 {
		t.Fatalf("unexpected issues: %v", issues)
	}
	if cookies[0].Format != "json" {
		t.Errorf("format = %q, want json", cookies[0].Format)
	}
}

func TestParseJSONArray(t *testing.T) {
	input := `[{"name":"MSPAuth","value":"A"},{"name":"MSPProf","value":"B"},{"name":"ANON","value":"C"}]`
	cookies, issues := ParseBlock(input)
	if len(issues) != 0 {
		t.Fatalf("unexpected issues: %v", issues)
	}
	if len(cookies) != 1 {
		t.Fatalf("want 1 cookie, got %d", len(cookies))
	}
	if cookies[0].Values["MSPProf"] != "B" {
		t.Errorf("MSPProf = %q", cookies[0].Values["MSPProf"])
	}
}

func TestParseNetscape(t *testing.T) {
	input := "# Netscape HTTP Cookie File\n" +
		".live.com\tTRUE\t/\tTRUE\t1800000000\tMSPAuth\tA\n" +
		".live.com\tTRUE\t/\tTRUE\t1800000000\tMSPProf\tB\n" +
		".live.com\tTRUE\t/\tTRUE\t1800000000\tANON\tC\n"
	cookies, issues := ParseBlock(input)
	if len(issues) != 0 {
		t.Fatalf("unexpected issues: %v", issues)
	}
	if len(cookies) != 1 {
		t.Fatalf("want 1 cookie, got %d", len(cookies))
	}
	if cookies[0].Format != "netscape" {
		t.Errorf("format = %q, want netscape", cookies[0].Format)
	}
	if cookies[0].Values["ANON"] != "C" {
		t.Errorf("ANON = %q", cookies[0].Values["ANON"])
	}
}

func TestMissingRequired(t *testing.T) {
	cookies, issues := ParseBlock("MSPAuth=A; MSPProf=B")
	if len(cookies) != 0 {
		t.Fatalf("want 0 cookies, got %d", len(cookies))
	}
	if len(issues) != 1 {
		t.Fatalf("want 1 issue, got %d", len(issues))
	}
}

func TestHeaderRoundTrip(t *testing.T) {
	cookies, _ := ParseBlock(rawCookie)
	h := cookies[0].Header()
	for _, want := range []string{"MSPAuth=ABC123", "MSPProf=DEF456", "ANON=GHI789"} {
		if !contains(h, want) {
			t.Errorf("header %q missing %q", h, want)
		}
	}
}

func TestExtractIdentityFromTitle(t *testing.T) {
	body := `<html><head><title>Inbox - someone@hotmail.com - Outlook</title></head></html>`
	email, _ := ExtractIdentity(body)
	if email != "someone@hotmail.com" {
		t.Errorf("email = %q", email)
	}
}

func TestExtractIdentityFromJSON(t *testing.T) {
	body := `{"DisplayName":"Jane Doe","SmtpAddress":"jane.doe@outlook.com"}`
	email, name := ExtractIdentity(body)
	if email != "jane.doe@outlook.com" {
		t.Errorf("email = %q", email)
	}
	if name != "Jane Doe" {
		t.Errorf("name = %q", name)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
