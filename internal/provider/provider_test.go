package provider

import "testing"

func TestHeaderSortsAndJoins(t *testing.T) {
	c := &Cookie{Values: map[string]string{"b": "2", "a": "1", "c": "3"}}
	if got, want := c.Header(), "a=1; b=2; c=3"; got != want {
		t.Errorf("Header() = %q, want %q", got, want)
	}
}

func TestHeaderStripsControlCharacters(t *testing.T) {
	c := &Cookie{Values: map[string]string{
		"sessionid": "abc\ndef\rX",
		"clean":     "ok",
		"bad\nkey":  "v",
	}}

	got := c.Header()

	for _, bad := range []string{"\n", "\r", "bad\nkey"} {
		if contains(got, bad) {
			t.Errorf("Header() still contains %q: %q", bad, got)
		}
	}
	if !contains(got, "sessionid=abcdefX") {
		t.Errorf("expected control chars removed from value, got %q", got)
	}
	if !contains(got, "clean=ok") {
		t.Errorf("clean cookie lost: %q", got)
	}
}

func TestLabelPrefersKnownCookies(t *testing.T) {
	c := &Cookie{Values: map[string]string{"NetflixId": "0123456789abcdef"}}
	if got := c.Label(); got != "0123456789ab…" {
		t.Errorf("Label() = %q", got)
	}
}

func TestNilCookieSafe(t *testing.T) {
	var c *Cookie
	if c.Header() != "" || c.Has("x") || c.Label() != "cookie" {
		t.Error("nil cookie should be inert")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
