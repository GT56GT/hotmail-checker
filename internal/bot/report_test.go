package bot

import (
	"strings"
	"testing"
	"time"

	"github.com/GT56GT/hotmail-checker/internal/archive"
	"github.com/GT56GT/hotmail-checker/internal/checker"
	"github.com/GT56GT/hotmail-checker/internal/job"
)

func TestParseIntakeZip(t *testing.T) {
	raw, err := archive.Build([]archive.File{
		{Name: "logs/a.txt", Data: []byte("MSPAuth=A; MSPProf=B; ANON=C\n")},
		{Name: "logs/b.txt", Data: []byte("MSPAuth=D; MSPProf=E; ANON=F\n")},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	in := parseIntake("logs.zip", raw)
	if len(in.cookies) != 2 {
		t.Fatalf("got %d cookies, want 2 (issues: %v)", len(in.cookies), in.issues)
	}
	if !strings.Contains(in.source, "logs.zip") {
		t.Errorf("source = %q", in.source)
	}
}

func TestParseIntakeDedupesAcrossFiles(t *testing.T) {
	line := "MSPAuth=A; MSPProf=B; ANON=C\n"
	raw, err := archive.Build([]archive.File{
		{Name: "a.txt", Data: []byte(line)},
		{Name: "b.txt", Data: []byte(line)},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	in := parseIntake("dup.zip", raw)
	if len(in.cookies) != 1 {
		t.Fatalf("got %d cookies, want 1", len(in.cookies))
	}
}

func TestParseIntakePlainText(t *testing.T) {
	in := parseIntake("pasted", []byte("MSPAuth=A; MSPProf=B; ANON=C\nMSPAuth=D; MSPProf=E; ANON=F\n"))
	if len(in.cookies) != 2 {
		t.Fatalf("got %d cookies, want 2", len(in.cookies))
	}
}

func TestBuildBundle(t *testing.T) {
	j := &job.Job{ID: "1-2", Total: 3, Source: "logs.zip"}
	j.Started = time.Now().Add(-time.Second)

	hits := []job.Hit{
		{Email: "a@hotmail.com", Name: "Jane", Cookie: "MSPAuth=x; MSPProf=y; ANON=z", Proxy: "1.2.3.4:8080"},
	}
	searches := []searchOutcome{{
		Email:  "a@hotmail.com",
		Method: "graph",
		Items: []checker.SearchItem{{
			Subject: "Invoice due",
			From:    "billing@vendor.com",
			Date:    time.Now(),
			Preview: "attached is invoice",
			Link:    "https://outlook.live.com/x",
		}},
	}}

	b := buildBundle(hits,
		[]string{"abc123"},
		[]string{"challenge: interaction_required"},
		[]string{"12 × timeout"},
		searches, "invoice", j)

	for _, want := range []string{
		"a@hotmail.com", "Jane", "1.2.3.4:8080",
		"Invoice due", "billing@vendor.com",
		"abc123", "challenge: interaction_required", "timeout",
		"logs.zip", "invoice",
	} {
		if !strings.Contains(b.report, want) {
			t.Errorf("report missing %q", want)
		}
	}

	if !strings.Contains(b.accounts, "a@hotmail.com") || !strings.Contains(b.accounts, "email,name") {
		t.Errorf("accounts.csv wrong:\n%s", b.accounts)
	}
	if !strings.Contains(b.search, "Invoice due") {
		t.Errorf("search.csv missing subject:\n%s", b.search)
	}
	if !strings.Contains(b.cookies, "MSPAuth=x") {
		t.Errorf("cookies.txt missing cookie:\n%s", b.cookies)
	}

	zipped, err := zipBundle(b, "invoice")
	if err != nil {
		t.Fatalf("zipBundle: %v", err)
	}

	res, err := archive.Extract(zipped, archive.DefaultLimits())
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}

	names := map[string]bool{}
	for _, f := range res.Files {
		names[f.Name] = true
	}
	for _, want := range []string{"report.txt", "cookies.txt", "accounts.csv", "dead.txt", "search.csv"} {
		if !names[want] {
			t.Errorf("bundle missing %q (has %v)", want, names)
		}
	}
}
