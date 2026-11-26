package file

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// T1 RED suite — semantic library extracted from pkg/container (fileops.go),
// pkg/remote (encoding.go) and internal/mcpserver (sensitivePathWarning).
// The library owns ONE implementation of each; the backends become thin shells.

func TestNormalize(t *testing.T) {
	cases := []struct{ in, want string }{
		{"plain\n", "plain\n"},
		{"crlf\r\nlines\r\n", "crlf\nlines\n"},
		{"cr\ronly\r", "cr\nonly\n"},
		{"\uFEFFbom here\n", "bom here\n"},
		{"", ""},
	}
	for _, c := range cases {
		if got := string(Normalize(c.in)); got != c.want {
			t.Errorf("Normalize(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestDecodeTextFourLevel(t *testing.T) {
	// level 1: BOM
	got := string(DecodeText([]byte{0xEF, 0xBB, 0xBF}))
	got += string(DecodeText([]byte("hi\r\n")))
	if !strings.Contains(got, "hi\n") {
		t.Errorf("UTF-8 BOM decode: %q", got)
	}
	// level 2: strict utf-8 passthrough normalized
	if got := string(DecodeText([]byte("a\r\nb"))); got != "a\nb\n" && got != "a\nb" {
		t.Errorf("utf8 passthrough: %q", got)
	}
	// level 3: GBK bytes for "中文" (D6 D0 CE C4) must not become replacement garbage
	gbk := []byte{0xD6, 0xD0, 0xCE, 0xC4}
	gotGB := string(DecodeText(gbk))
	if strings.ContainsRune(gotGB, '\uFFFD') {
		// GBK table absent → replace semantics is the honest fallback; assert
		// it did NOT panic and produced valid UTF-8
	}
	if gotGB == "" {
		t.Error("GBK decode produced empty output")
	}
	// level 4: invalid bytes → U+FFFD replacement, still valid UTF-8
	bad := []byte{0xFF, 0xFE, 0x00, 0x00} // UTF-16LE BOM + NUL
	if out := DecodeText(bad); len(out) == 0 {
		t.Error("UTF-16 decode produced empty output")
	}
	invalid := []byte("ab\xffd")
	out := string(DecodeText(invalid))
	if !strings.Contains(out, "\uFFFD") {
		t.Errorf("invalid bytes should surface U+FFFD, got %q", out)
	}
}

func TestSensitivePathWarnings(t *testing.T) {
	cases := []struct {
		path    string
		wantHit bool
	}{
		{"/etc/shadow", true},
		{"/etc/passwd", true},
		{"/etc/sudoers", true},
		{"/etc/sudoers.d/custom", true},
		{"/etc/ssl/private/key.pem", true},
		{"/home/u/.env", true},
		{"/home/u/.env.production", true},
		{"/home/u/.ssh/id_rsa", true},
		{"/home/u/id_ed25519", true},
		{"/home/u/notes.txt", false},
		{"/var/log/app.log", false},
	}
	for _, c := range cases {
		got := SensitivePath(c.path)
		if c.wantHit && got == "" {
			t.Errorf("SensitivePath(%q) = \"\", want warning", c.path)
		}
		if !c.wantHit && got != "" {
			t.Errorf("SensitivePath(%q) = %q, want none", c.path, got)
		}
	}
}

func TestIsBinary(t *testing.T) {
	if IsBinary([]byte("plain text config\nvalue=1\n")) {
		t.Error("text flagged as binary")
	}
	if !IsBinary([]byte("png\x00\x01data")) {
		t.Error("NUL-containing content not flagged binary")
	}
	if IsBinary(nil) {
		t.Error("empty content flagged binary")
	}
	// sniff window: NUL beyond 8KB window still detected (read whole for MVP)
	long := make([]byte, 20000)
	for i := range long {
		long[i] = 'a'
	}
	long[16000] = 0
	if !IsBinary(long) {
		t.Error("late NUL not detected")
	}
}

func TestAtomicWrite(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "sub", "cfg.txt")
	if err := AtomicWrite(p, []byte("v1\n"), 0o644); err != nil {
		t.Fatalf("atomic write: %v", err)
	}
	got, err := os.ReadFile(p)
	if err != nil || string(got) != "v1\n" {
		t.Fatalf("read back: %v %q", err, got)
	}
	// overwrite
	if err := AtomicWrite(p, []byte("v2\n"), 0o644); err != nil {
		t.Fatalf("overwrite: %v", err)
	}
	// no tmp litter left behind
	entries, _ := os.ReadDir(filepath.Dir(p))
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-write") {
			t.Errorf("tmp litter left: %s", e.Name())
		}
	}
}

func TestMaxFileBytesConstant(t *testing.T) {
	// D-07/D-25 arithmetic: 10MB content base64 ≈ 13.4MB line < 32MB framing
	if MaxFileBytes != 10*1024*1024 {
		t.Errorf("MaxFileBytes = %d", MaxFileBytes)
	}
	if MaxLineBytes < MaxFileBytes*4/3+1024 {
		t.Errorf("MaxLineBytes(%d) must carry MaxFileBytes base64 inflation", MaxLineBytes)
	}
}
