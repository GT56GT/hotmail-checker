package archive

import (
	"bytes"
	"testing"
)

func makeZip(t *testing.T, entries map[string]string) []byte {
	t.Helper()

	var files []File
	for name, body := range entries {
		files = append(files, File{Name: name, Data: []byte(body)})
	}
	data, err := Build(files)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return data
}

func TestBuildAndExtract(t *testing.T) {
	raw := makeZip(t, map[string]string{
		"logs/a.txt":     "MSPAuth=A; MSPProf=B; ANON=C\n",
		"logs/b.txt":     "MSPAuth=D; MSPProf=E; ANON=F\n",
		"notes/empty.md": "   ",
	})

	if !LooksZipped(raw) {
		t.Fatal("LooksZipped = false")
	}

	res, err := Extract(raw, DefaultLimits())
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(res.Files) != 2 {
		t.Fatalf("got %d files, want 2: %v", len(res.Files), res.Skipped)
	}
}

func TestExtractRejectsNonZip(t *testing.T) {
	if _, err := Extract([]byte("not a zip at all"), DefaultLimits()); err == nil {
		t.Fatal("expected error for non-zip input")
	}
}

func TestExtractSkipsBinary(t *testing.T) {
	raw := makeZip(t, map[string]string{
		"good.txt": "MSPAuth=A; MSPProf=B; ANON=C",
		"bad.bin":  "text\x00with\x00nulls",
	})

	res, err := Extract(raw, DefaultLimits())
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(res.Files) != 1 {
		t.Fatalf("got %d files, want 1", len(res.Files))
	}
	if res.Files[0].Name != "good.txt" {
		t.Errorf("kept %q", res.Files[0].Name)
	}
	if len(res.Skipped) != 1 {
		t.Errorf("skipped = %v", res.Skipped)
	}
}

func TestExtractHonoursFileLimit(t *testing.T) {
	raw := makeZip(t, map[string]string{
		"1.txt": "a",
		"2.txt": "b",
		"3.txt": "c",
	})

	lim := DefaultLimits()
	lim.MaxFiles = 2

	res, err := Extract(raw, lim)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(res.Files) != 2 {
		t.Fatalf("got %d files, want 2", len(res.Files))
	}
	if len(res.Skipped) != 1 {
		t.Errorf("skipped = %v", res.Skipped)
	}
}

func TestExtractSkipsPathTraversal(t *testing.T) {
	raw := makeZip(t, map[string]string{
		"../escape.txt": "boom",
		"ok.txt":        "fine",
	})

	res, err := Extract(raw, DefaultLimits())
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	for _, f := range res.Files {
		if bytes.Contains([]byte(f.Name), []byte("..")) {
			t.Errorf("kept traversal entry %q", f.Name)
		}
	}
}
