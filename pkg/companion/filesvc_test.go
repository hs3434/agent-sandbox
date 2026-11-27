package companion

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFilePatchSemantics(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	os.WriteFile(path, []byte("alpha beta alpha"), 0o644)
	// ambiguity rule (orchestrator parity): >1 match without all → refuse
	if _, err := FilePatch(path, "alpha", "X", false); err == nil {
		t.Fatal("ambiguous patch must refuse without all=true")
	}
	n, err := FilePatch(path, "alpha", "X", true)
	if err != nil || n != 2 {
		t.Fatalf("all patch: n=%d err=%v", n, err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "X beta X" {
		t.Fatalf("content = %q", got)
	}
	// zero matches → typed refusal
	if _, err := FilePatch(path, "zzz", "Y", false); err == nil {
		t.Fatal("zero-match patch must refuse")
	}
}

func TestFileSearchTargetLocal(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("needle here\nother\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "b.log"), []byte("needle too\n"), 0o644)
	out, diag, err := FileSearch(dir, "needle", "", 50)
	if err != nil {
		t.Fatalf("search: %v diag=%s", err, diag)
	}
	if len(out) < 2 || !contains(out, "a.txt") || !contains(out, "b.log") {
		t.Fatalf("matches = %q", out)
	}
	// glob filter
	out2, _, _ := FileSearch(dir, "needle", "*.txt", 50)
	if contains(out2, "b.log") {
		t.Fatalf("glob leaked: %q", out2)
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
