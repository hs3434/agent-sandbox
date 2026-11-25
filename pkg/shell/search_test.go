package shell

import (
	"context"
	"strings"
	"testing"
)

// fakeExec answers with canned output per command shape.
func fakeExec(t *testing.T, responses map[string]string) ExecFunc {
	t.Helper()
	return func(_ context.Context, cmd string) (string, int, error) {
		for k, v := range responses {
			if strings.Contains(cmd, k) {
				return v, 0, nil
			}
		}
		return "", 1, nil
	}
}

func TestSearchContentSeparatesDiagnostics(t *testing.T) {
	run := fakeExec(t, map[string]string{
		"rg ": "app/main.go:12:needle here\nrg: /secret: Permission denied\n",
	})
	res, err := Search(context.Background(), run, PosixDialect{}, SearchParams{
		Pattern: "needle", Path: ".", SearchType: "content",
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(res.Matches) != 1 || !strings.Contains(res.Matches[0], "needle") {
		t.Fatalf("matches = %v, want the payload line only", res.Matches)
	}
	if !strings.Contains(res.Diagnostics, "Permission denied") {
		t.Fatalf("diagnostics = %q, want the rg: line separated out", res.Diagnostics)
	}
	if res.Engine != "rg" {
		t.Fatalf("engine = %q, want rg", res.Engine)
	}
}

func TestSearchFallsBackToGrepWhenRGMissing(t *testing.T) {
	run := fakeExec(t, map[string]string{
		"rg ":     "sh: rg: not found\n",
		"grep -r": "app/main.go:12:needle\n",
	})
	res, err := Search(context.Background(), run, PosixDialect{}, SearchParams{
		Pattern: "needle", Path: ".", SearchType: "content",
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if res.Engine != "grep" {
		t.Fatalf("engine = %q, want grep fallback", res.Engine)
	}
	if len(res.Matches) != 1 {
		t.Fatalf("matches = %v", res.Matches)
	}
}

func TestSearchFilesModeAndPagination(t *testing.T) {
	run := fakeExec(t, map[string]string{
		"rg --files": "a.py\nb.py\nc.py\nd.py\n",
	})
	res, err := Search(context.Background(), run, PosixDialect{}, SearchParams{
		Pattern: "*.py", Path: ".", SearchType: "files", Limit: 2,
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(res.Matches) != 2 {
		t.Fatalf("matches = %v, want 2 (limit)", res.Matches)
	}
	if !res.Truncated {
		t.Fatal("truncated must be set when the fetch limit was reached")
	}
	if res.Type != "files" {
		t.Fatalf("type = %q", res.Type)
	}
}

func TestSearchOffsetSlices(t *testing.T) {
	run := fakeExec(t, map[string]string{"rg ": "a\nb\nc\nd\n"})
	res, err := Search(context.Background(), run, PosixDialect{}, SearchParams{
		Pattern: "x", Path: ".", Limit: 2, Offset: 1,
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(res.Matches) != 2 || res.Matches[0] != "b" {
		t.Fatalf("matches = %v, want [b c]", res.Matches)
	}
}

func TestSearchRejectsUnknownType(t *testing.T) {
	_, err := Search(context.Background(), fakeExec(t, nil), PosixDialect{},
		SearchParams{Pattern: "x", SearchType: "fuzzy"})
	if err == nil || !strings.Contains(err.Error(), "search_type") {
		t.Fatalf("err = %v, want a search_type rejection", err)
	}
}

func TestSearchErrorsWhenNoEngineExists(t *testing.T) {
	run := fakeExec(t, map[string]string{"rg ": "sh: rg: not found\n", "grep ": "sh: grep: not found\n"})
	_, err := Search(context.Background(), run, PosixDialect{}, SearchParams{Pattern: "x"})
	if err == nil || !strings.Contains(err.Error(), "ripgrep") {
		t.Fatalf("err = %v, want the not-installed error", err)
	}
}

func TestDialectCommandShapes(t *testing.T) {
	posix := PosixDialect{}
	if !strings.Contains(posix.SearchFilesCommand("py", ".", 10), "rg --files") {
		t.Fatalf("posix files cmd = %q", posix.SearchFilesCommand("py", ".", 10))
	}
	if !strings.Contains(posix.SearchContentCommand("x", ".", "*.go", 5, "files_only", 2), "-l") {
		t.Fatalf("posix content cmd = %q", posix.SearchContentCommand("x", ".", "*.go", 5, "files_only", 2))
	}
	ps := PowerShellDialect{}
	if !strings.Contains(ps.SearchFilesCommand("py", ".", 10), "Select-Object") {
		t.Fatalf("ps files cmd = %q", ps.SearchFilesCommand("py", ".", 10))
	}
	if !strings.Contains(ps.FrameCommand("echo hi", "M1"), "$LASTEXITCODE") {
		t.Fatalf("ps frame = %q", ps.FrameCommand("echo hi", "M1"))
	}
	if !strings.Contains(posix.FrameCommand("echo hi", "M1"), "__SB_END_M1__") {
		t.Fatalf("posix frame = %q", posix.FrameCommand("echo hi", "M1"))
	}
}
