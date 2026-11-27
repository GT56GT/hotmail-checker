package bot

import (
	"strings"
	"testing"
	"time"

	"github.com/GT56GT/hotmail-checker/internal/archive"
	"github.com/GT56GT/hotmail-checker/internal/checker"
	"github.com/GT56GT/hotmail-checker/internal/job"
	"github.com/GT56GT/hotmail-checker/internal/netflix"
	"github.com/GT56GT/hotmail-checker/internal/provider"
)

func testCheckers() []provider.Checker {
	return []provider.Checker{
		checker.New(10*time.Second, 1),
		netflix.New(10 * time.Second),
	}
}

func TestParseIntakeZip(t *testing.T) {
	raw, err := archive.Build([]archive.File{
		{Name: "logs/a.txt", Data: []byte("MSPAuth=A; MSPProf=B; ANON=C\n")},
		{Name: "logs/b.txt", Data: []byte("MSPAuth=D; MSPProf=E; ANON=F\n")},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	in := parseIntake("logs.zip", raw, testCheckers())
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

	in := parseIntake("dup.zip", raw, testCheckers())
	if len(in.cookies) != 1 {
		t.Fatalf("got %d cookies, want 1", len(in.cookies))
	}
}

func TestParseIntakeMixedProviders(t *testing.T) {
	raw, err := archive.Build([]archive.File{
		{Name: "ms.txt", Data: []byte("MSPAuth=A; MSPProf=B; ANON=C\n")},
		{Name: "nf.txt", Data: []byte("NetflixId=NF1; SecureNetflixId=SNF1\n")},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	in := parseIntake("mixed.zip", raw, testCheckers())
	if len(in.cookies) != 2 {
		t.Fatalf("got %d cookies, want 2 (issues: %v)", len(in.cookies), in.issues)
	}
	if in.services["microsoft"] != 1 || in.services["netflix"] != 1 {
		t.Errorf("services = %v", in.services)
	}
	if !strings.Contains(in.serviceSummary(), "Netflix 1") {
		t.Errorf("summary = %q", in.serviceSummary())
	}
}

func TestParseIntakePlainText(t *testing.T) {
	in := parseIntake("pasted", []byte("MSPAuth=A; MSPProf=B; ANON=C\nMSPAuth=D; MSPProf=E; ANON=F\n"), testCheckers())
	if len(in.cookies) != 2 {
		t.Fatalf("got %d cookies, want 2", len(in.cookies))
	}
}

func TestBuildBundle(t *testing.T) {
	j := &job.Job{ID: "1-2", Total: 3, Source: "logs.zip"}
	j.Started = time.Now().Add(-time.Second)

	hits := []job.Hit{
		{
			Provider: "netflix",
			Email:    "jane@example.com",
			Name:     "Jane",
			Cookie:   "NetflixId=NF; SecureNetflixId=SNF",
			Proxy:    "1.2.3.4:8080",
			Meta: map[string]string{
				"Plan":    "Premium",
				"Country": "US",
			},
		},
		{
			Provider: "microsoft",
			Email:    "a@hotmail.com",
			Cookie:   "MSPAuth=x; MSPProf=y; ANON=z",
			Proxy:    "1.2.3.4:8080",
			Meta:     map[string]string{},
		},
	}

	searches := map[int]searchOutcome{
		1: {
			Email:  "a@hotmail.com",
			Method: "graph",
			Items: []provider.SearchItem{{
				Subject: "Invoice due",
				From:    "billing@vendor.com",
				Date:    time.Now(),
				Preview: "attached is invoice",
				Link:    "https://outlook.live.com/x",
			}},
		},
	}

	b := buildBundle(hits,
		[]string{"abc123"},
		[]string{"Microsoft: challenge: interaction_required"},
		[]string{"12 × timeout"},
		searches, "invoice", j)

	for _, want := range []string{
		"jane@example.com", "Netflix", "Premium", "US",
		"a@hotmail.com", "Microsoft",
		"Invoice due", "billing@vendor.com",
		"abc123", "challenge: interaction_required", "timeout",
		"logs.zip", "invoice",
	} {
		if !strings.Contains(b.report, want) {
			t.Errorf("report missing %q", want)
		}
	}

	if !strings.Contains(b.accounts, "provider,email") {
		t.Errorf("accounts.csv header wrong:\n%s", b.accounts)
	}
	if !strings.Contains(b.accounts, "netflix") {
		t.Errorf("accounts.csv missing provider:\n%s", b.accounts)
	}
	if !strings.Contains(b.search, "Invoice due") {
		t.Errorf("search.csv missing subject:\n%s", b.search)
	}
	if !strings.Contains(b.cookies, "NetflixId=NF") || !strings.Contains(b.cookies, "MSPAuth=x") {
		t.Errorf("cookies.txt missing entries:\n%s", b.cookies)
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
