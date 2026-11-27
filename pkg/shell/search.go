package shell

import (
	"context"
	"fmt"
	"strings"
)

// ExecFunc runs a command on the target and returns its combined output.
type ExecFunc func(ctx context.Context, cmd string) (output string, exitCode int, err error)

// SearchParams are the file_search inputs (ported from the Python design).
type SearchParams struct {
	Pattern     string
	Path        string
	SearchType  string // "content" (default) | "files"
	FileGlob    string
	Limit       int
	Offset      int
	OutputMode  string // "content" | "files_only" | "count"
	Context     int
	TimeoutSecs int // per-command timeout; 0 = engine default (60s), ceiling 300s (D-27)
}

// SearchResult is the file_search payload.
type SearchResult struct {
	Matches     []string `json:"matches"`
	Diagnostics string   `json:"diagnostics,omitempty"`
	Truncated   bool     `json:"truncated"`
	Type        string   `json:"type"`   // content | files
	Engine      string   `json:"engine"` // rg | grep — which tool produced it
}

// Error kinds returned as errors by Search (the tool face maps them to
// structured tool errors).
var (
	// ErrRGNotFound means ripgrep is not installed on the target.
	ErrRGNotFound = fmt.Errorf("ripgrep (rg) is not installed on the target machine")
	// ErrUnknownSearchType means the caller passed a bogus search_type.
	ErrUnknownSearchType = fmt.Errorf("unknown search_type")
)

// rgNotFoundPatterns are the ways the target shells report a missing rg.
var rgNotFoundPatterns = []string{
	"is not recognized", // PowerShell EN
	"未识别",               // PowerShell ZH
	"项识别",               // PowerShell ZH (short)
	"无法将",               // PowerShell ZH
	"cannot find",       // PowerShell / CMD
	"command not found", // bash
	"not found",         // generic
}

// Search runs ripgrep ON THE TARGET and separates diagnostics from payload
// (ported from the Python predecessor: rg diagnostics look like "rg: <path>:
// <message>" and must not be parsed as matches).
func Search(ctx context.Context, run ExecFunc, d Dialect, p SearchParams) (*SearchResult, error) {
	if p.SearchType == "" {
		p.SearchType = "content"
	}
	if p.SearchType != "content" && p.SearchType != "files" {
		return nil, fmt.Errorf("%w: %q (want content|files)", ErrUnknownSearchType, p.SearchType)
	}
	if p.Limit <= 0 {
		p.Limit = 50
	}
	if p.Offset < 0 {
		p.Offset = 0
	}
	if p.Path == "" {
		p.Path = "."
	}
	fetch := p.Limit + p.Offset // over-fetch, then slice

	build := func(d Dialect, fallback bool) string {
		if p.SearchType == "files" {
			if fallback {
				return d.SearchFilesCommandFallback(p.Pattern, p.Path, fetch)
			}
			return d.SearchFilesCommand(p.Pattern, p.Path, fetch)
		}
		if fallback {
			return d.SearchContentCommandFallback(p.Pattern, p.Path, p.FileGlob, fetch, p.OutputMode, p.Context)
		}
		return d.SearchContentCommand(p.Pattern, p.Path, p.FileGlob, fetch, p.OutputMode, p.Context)
	}

	// ripgrep first; stock images ship grep, not rg — degrade instead of
	// failing (capability release, §0: 不拦命令).
	raw, _, err := run(ctx, build(d, false))
	if err != nil {
		return nil, err
	}
	engine := "rg"
	if isRGMissing(raw) {
		raw, _, err = run(ctx, build(d, true))
		if err != nil {
			return nil, err
		}
		if isRGMissing(raw) {
			return nil, fmt.Errorf("%w — file_search requires rg (or grep) on the target.\n%s", ErrRGNotFound, d.MissingToolHint())
		}
		engine = "grep"
	}
	out := raw
	diagnostics, payload := splitDiagnostics(out)
	lines := splitLines(payload)
	truncated := len(lines) >= fetch
	if truncated {
		lines = lines[:fetch]
	}
	// offset slice (pagination)
	if p.Offset > 0 {
		if p.Offset >= len(lines) {
			lines = nil
		} else {
			lines = lines[p.Offset:]
		}
	}
	if len(lines) > p.Limit {
		lines = lines[:p.Limit]
	}
	return &SearchResult{
		Matches:     lines,
		Diagnostics: diagnostics,
		Truncated:   truncated,
		Type:        p.SearchType,
		Engine:      engine,
	}, nil
}

// splitDiagnostics separates tool diagnostics from payload lines.
func splitDiagnostics(output string) (diagnostics, payload string) {
	var d, p []string
	for _, line := range splitLines(output) {
		if strings.HasPrefix(line, "rg:") || strings.HasPrefix(line, "grep:") {
			d = append(d, line)
			continue
		}
		p = append(p, line)
	}
	return strings.Join(d, "\n"), strings.Join(p, "\n")
}

// isRGMissing reports whether the output says "no such tool: rg".
func isRGMissing(output string) bool {
	low := strings.ToLower(output)
	for _, pat := range rgNotFoundPatterns {
		if strings.Contains(low, pat) {
			return true
		}
	}
	return false
}

func splitLines(s string) []string {
	var out []string
	for _, l := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n") {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}
