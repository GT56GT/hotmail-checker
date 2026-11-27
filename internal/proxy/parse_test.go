package proxy

import "testing"

func TestNormaliseFormats(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"1.2.3.4:8080", "http://1.2.3.4:8080"},
		{"1.2.3.4:8080:user:pass", "http://user:pass@1.2.3.4:8080"},
		{"user:pass@1.2.3.4:8080", "http://user:pass@1.2.3.4:8080"},
		{"socks5://1.2.3.4:1080", "socks5://1.2.3.4:1080"},
		{"socks5://user:pass@1.2.3.4:1080", "socks5://user:pass@1.2.3.4:1080"},
		{"http://1.2.3.4:8080", "http://1.2.3.4:8080"},
		{"https://1.2.3.4:8443", "https://1.2.3.4:8443"},
		{"socks4://1.2.3.4:1080", "socks4://1.2.3.4:1080"},
		{"[2001:db8::1]:8080", "http://[2001:db8::1]:8080"},
		{"socks5://[::1]:1080", "socks5://[::1]:1080"},
		{"  host.example:3128  ", "http://host.example:3128"},
	}

	for _, c := range cases {
		got, err := Normalise(c.in)
		if err != nil {
			t.Errorf("Normalise(%q) error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("Normalise(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestNormaliseRejects(t *testing.T) {
	bad := []string{
		"",
		"just-a-host",
		"1.2.3.4",
		"ftp://1.2.3.4:21",
		"1.2.3.4:8080:user",
		"[::1]",
	}

	for _, in := range bad {
		if got, err := Normalise(in); err == nil {
			t.Errorf("Normalise(%q) = %q, want error", in, got)
		}
	}
}

func TestParseListMixed(t *testing.T) {
	input := `# my proxies
1.2.3.4:8080
1.2.3.5:8080:user:pass
socks5://user:pass@1.2.3.6:1080, 1.2.3.7:3128; https://1.2.3.8:8443
garbage-entry
1.2.3.4:8080`

	urls, rejected := ParseList(input)

	if len(urls) != 5 {
		t.Fatalf("got %d urls, want 5 (last entry duplicates the first): %v", len(urls), urls)
	}
	if len(rejected) != 1 {
		t.Fatalf("got %d rejected, want 1: %v", len(rejected), rejected)
	}
	if rejected[0] != "garbage-entry" {
		t.Errorf("rejected[0] = %q", rejected[0])
	}
}

func TestParseListDedupes(t *testing.T) {
	urls, _ := ParseList("1.2.3.4:8080\nhttp://1.2.3.4:8080\n1.2.3.4:8080")
	if len(urls) != 1 {
		t.Fatalf("got %d urls, want 1: %v", len(urls), urls)
	}
}

func TestLabelHidesCredentials(t *testing.T) {
	label := Label("socks5://user:secret@1.2.3.4:1080")
	if label != "socks5://1.2.3.4:1080 (auth)" {
		t.Errorf("Label = %q", label)
	}
}
