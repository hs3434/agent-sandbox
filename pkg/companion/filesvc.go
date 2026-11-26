// filesvc serves pkg/file semantics over the companion protocol (T9,
// D-01/D-07/D-08/D-09/D-27): read/write with the 10MB cap enforced
// SERVER-side (D-27: a second client cannot bypass), binary detection with
// bounded refusal advice, path freedom (whole machine — the safe-box, not
// a fence), and durability disclosure (companion files survive the daemon).
package companion

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
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
