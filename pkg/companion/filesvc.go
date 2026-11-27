// filesvc serves pkg/file semantics over the companion protocol (T9,
// D-01/D-07/D-08/D-09/D-27): read/write with the 10MB cap enforced
// SERVER-side (D-27: a second client cannot bypass), binary detection with
// bounded refusal advice, path freedom (whole machine — the safe-box, not
// a fence), and durability disclosure (companion files survive the daemon).
package companion

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// FileLimits mirrors pkg/file's contract caps (declared here so the server
// binary need not import pkg/file — one semantic copy, protocol-owned).
const (
	MaxFileBytes = 10 * 1024 * 1024 // 10MB payload ceiling (D-07)
)

// ErrFileTooBig / ErrBinary are typed refusal envelopes (bounded advice,
// D-30: the text itself must not become an attack vector).
func ErrFileTooBig(n int) error {
	return fmt.Errorf("file too big: %d bytes exceeds the %d byte protocol cap "+
		"(10MB base64-inflated); use shell escape for bigger payloads", n, MaxFileBytes)
}

func ErrBinary(path string) error {
	return fmt.Errorf("binary file refused: %s looks binary (NUL in first 8KB); "+
		"reading it would burn agent context for nothing — use shell (strings/head) "+
		"if you truly need a slice", path)
}

// isBinary sniffs NUL bytes in the first 8KB (same rule as pkg/file — the
// two implementations are pinned together by the contract test in T10).
func isBinary(data []byte) bool {
	n := len(data)
	if n > 8192 {
		n = 8192
	}
	for _, b := range data[:n] {
		if b == 0 {
			return true
		}
	}
	return false
}

// FileRead loads a whole file, enforces the cap, sniffs binary, decodes
// text (the four-tier normalize ladder lives in pkg/file; over the wire we
// return base64 so encoding survives the transport).
func FileRead(path string) (b64 string, durable bool, err error) {
	fi, err := os.Stat(path)
	if err != nil {
		return "", false, fmt.Errorf("stat %s: %w", path, err)
	}
	if fi.Size() > MaxFileBytes {
		return "", false, ErrFileTooBig(int(fi.Size()))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false, err
	}
	if len(data) > MaxFileBytes {
		return "", false, ErrFileTooBig(len(data))
	}
	if isBinary(data) {
		return "", false, ErrBinary(path)
	}
	return base64.StdEncoding.EncodeToString(data), true, nil
}

// FileWrite persists atomically (tmp+rename in the target directory — the
// invariant pkg/file's AtomicWrite established, re-done locally because the
// companion binary is its own composition root).
func FileWrite(path, b64Content string) (durable bool, err error) {
	data, err := base64.StdEncoding.DecodeString(b64Content)
	if err != nil {
		return false, fmt.Errorf("content decode: %w", err)
	}
	if len(data) > MaxFileBytes {
		return false, ErrFileTooBig(len(data))
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".sb-write-*")
	if err != nil {
		return false, fmt.Errorf("tmp in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after success
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return false, err
	}
	if err := tmp.Sync(); err != nil { // durable=true must mean durable (parity with WriteConn)
		tmp.Close()
		return false, err
	}
	if err := tmp.Close(); err != nil {
		return false, err
	}
	// preserve an existing file's mode; new files get 0644
	mode := os.FileMode(0o644)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		return false, err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return false, fmt.Errorf("rename to %s: %w", path, err)
	}
	return true, nil
}

// FilePatch applies old→new on the target (the exact orchestrator semantics,
// one implementation served over the protocol): count matches, enforce the
// replace_all disambiguation rule, write back atomically.
func FilePatch(path, oldS, newS string, all bool) (int, error) {
	if oldS == "" {
		return 0, fmt.Errorf("old_string is required")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	text := string(data)
	n := strings.Count(text, oldS)
	if n == 0 {
		return 0, fmt.Errorf("old_string not found in %q", path)
	}
	if !all && n > 1 {
		return 0, fmt.Errorf("old_string matches %d times — pass replace_all or narrow it", n)
	}
	updated := strings.Replace(text, oldS, newS, -1)
	if _, err := FileWrite(path, base64.StdEncoding.EncodeToString([]byte(updated))); err != nil {
		return 0, err
	}
	return n, nil
}

// FileSearch runs rg→grep ON THE TARGET inside the daemon process (truth T5:
// the search executes where the files live). Output is raw match lines;
// diagnostics ride stderr separated by the caller. Bounded by the D-27
// 300s ceiling (armed per request).
// SearchCeiling is the D-27 server-side time cap (the platform's clamp
// mirrors it; a direct protocol client is bounded here too).
const SearchCeiling = 300 * time.Second

func FileSearch(root, pattern, fileGlob string, limit int) (string, string, error) {
	return fileSearchCeiled(context.Background(), root, pattern, fileGlob, limit)
}

func fileSearchCeiled(ctx context.Context, root, pattern, fileGlob string, limit int) (string, string, error) {
	ctx, cancel := context.WithTimeout(ctx, SearchCeiling)
	defer cancel()
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var stdout, stderr strings.Builder
	var runErr error
	if _, err := exec.LookPath("rg"); err == nil {
		// "--" terminates option parsing: a client-supplied pattern can never
		// become a flag (rg --pre=<cmd> would execute a program — D-30's
		// advice-as-attack-vector rule applies to argv too)
		args := []string{"rg", "--no-heading", "-n", "--color", "never", "-m", "200"}
		if fileGlob != "" {
			args = append(args, "--glob", fileGlob)
		}
		// "--" terminates option parsing: a client-supplied pattern can never
		// become a flag (rg --pre=<cmd> would execute a program — D-30's
		// advice-as-attack-vector rule applies to argv too)
		args = append(args, "--", pattern, root)
		args = append(args, "|", "head", "-n", fmt.Sprint(limit))
		runErr = pipeRunCtx(ctx, args, &stdout, &stderr)
	} else {
		// options (incl. --include) must precede "--": after it every token
		// is an operand, so a late --include would be searched as a FILE
		args := []string{"grep", "-rnE"}
		if fileGlob != "" {
			args = append(args, "--include", fileGlob)
		}
		args = append(args, "--", pattern, root)
		args = append(args, "|", "head", "-n", fmt.Sprint(limit))
		runErr = pipeRunCtx(ctx, args, &stdout, &stderr)
	}
	out := strings.TrimSpace(stdout.String())
	if out == "" && runErr != nil {
		return "", stderr.String(), runErr
	}
	return out, stderr.String(), nil
}

// pipeRunCtx runs a tokenized command under ctx (children die with it —
// the 300s ceiling actually kills rg, it doesn't just refuse entry).
func pipeRunCtx(ctx context.Context, args []string, stdout, stderr *strings.Builder) error {
	if i := indexOfStr(args, "|"); i >= 0 {
		left := exec.CommandContext(ctx, args[0], args[1:i]...)
		right := exec.CommandContext(ctx, args[i+1], args[i+2:]...)
		left.Stderr = stderr
		r, err := left.StdoutPipe()
		if err != nil {
			return err
		}
		right.Stdin = r
		right.Stdout = stdout
		right.Stderr = stderr
		if err := left.Start(); err != nil {
			return err
		}
		if err := right.Start(); err != nil {
			_ = left.Process.Kill()
			return err
		}
		errL := left.Wait()
		errR := right.Wait()
		if errR != nil {
			return errR
		}
		return errL
	}
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

func indexOfStr(args []string, tok string) int {
	for i, a := range args {
		if a == tok {
			return i
		}
	}
	return -1
}
