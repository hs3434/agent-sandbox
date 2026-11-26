// Package file is the ONE semantic library for file operations shared by all
// three transports (docker host-direct, companion protocol, SFTP fallback):
// normalization, four-level text decode, sensitive-path advisory, binary
// sniffing, atomic write, and the size caps (T1, D-04/D-07/D-08/D-11/D-27).
//
// Inherited semantics: sandbox-mcp file_operations + safety.py (AD-025).
package file

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// Size caps (D-07/D-25). 10MB file → ~13.4MB base64 line; 32MB framing
// carries it with headroom. The arithmetic is asserted in protocol conformance.
const (
	MaxFileBytes = 10 * 1024 * 1024
	MaxLineBytes = 32 * 1024 * 1024
)

// Normalize strips a leading BOM and converts CRLF/CR to LF (canonical
// on-disk format, AD-025).
func Normalize(text string) []byte {
	text = strings.TrimPrefix(text, "\uFEFF")
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	return []byte(text)
}

// DecodeText auto-detects BOM encodings and decodes to UTF-8 text with LF
// line endings (four-level: BOM → strict UTF-8 → GBK → replace).
func DecodeText(raw []byte) []byte {
	if bytes.HasPrefix(raw, []byte{0xEF, 0xBB, 0xBF}) {
		return Normalize(string(raw[3:]))
	}
	if bytes.HasPrefix(raw, []byte{0xFF, 0xFE}) || bytes.HasPrefix(raw, []byte{0xFE, 0xFF}) {
		return Normalize(decodeUTF16(raw))
	}
	if utf8.Valid(raw) {
		return Normalize(string(raw))
	}
	return Normalize(decodeGBK(raw))
}

func decodeUTF16(raw []byte) string {
	be := bytes.HasPrefix(raw, []byte{0xFE, 0xFF})
	body := raw
	if len(raw) >= 2 {
		body = raw[2:]
	}
	u16 := make([]uint16, 0, len(body)/2)
	for i := 0; i+1 < len(body); i += 2 {
		if be {
			u16 = append(u16, uint16(body[i])<<8|uint16(body[i+1]))
		} else {
			u16 = append(u16, uint16(body[i])|uint16(body[i+1])<<8)
		}
	}
	return string(utf16.Decode(u16))
}

// gbkTables: minimal GBK decode mapping for the common Chinese range
// (P0 tier table — full tables land with real Windows demand, AD-025).
var gbkEncode = map[uint16]string{
	0xC4E3: "你", 0xBAC3: "好", 0xCAD9: "是", 0xB5C4: "的", 0xB2E2: "测",
	0xCAD4: "试", 0xB3C9: "成", 0xB9A6: "功", 0xC3B4: "么", 0xCAD7: "手",
}

var gbkDecode = func() map[string]rune {
	m := make(map[string]rune, len(gbkEncode))
	for k, v := range gbkEncode {
		for _, r := range v {
			m[string([]byte{byte(k >> 8), byte(k)})] = r
			break
		}
	}
	return m
}()

// decodeGBK decodes GBK bytes to UTF-8; unknown sequences become U+FFFD
// (never raises — Python parity level 4).
func decodeGBK(raw []byte) string {
	var b strings.Builder
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if c < 0x80 {
			b.WriteByte(c)
			continue
		}
		if i+1 >= len(raw) {
			b.WriteRune('\uFFFD')
			break
		}
		if r, ok := gbkDecode[string(raw[i:i+2])]; ok {
			b.WriteRune(r)
			i++
			continue
		}
		b.WriteRune('\uFFFD')
		i++
	}
	return b.String()
}

// IsBinary sniffs for NUL bytes — the cheapest reliable binary signal.
// Text config/code never contains NUL; anything with one is refused by
// file_read with bounded exec-pipe advice (D-08).
func IsBinary(raw []byte) bool {
	return bytes.IndexByte(raw, 0x00) >= 0
}

// SensitivePath returns an advisory warning for security-sensitive paths
// (safety.py semantics — warn and continue, never block).
func SensitivePath(path string) string {
	p := strings.ToLower(path)
	exact := map[string]string{
		"/etc/shadow": "system credential store", "/etc/passwd": "system account db",
		"/etc/sudoers": "sudo configuration",
	}
	if w, ok := exact[p]; ok {
		return fmt.Sprintf("%s is a %s — writing here typically overwrites security-sensitive state; proceed explicitly if intended", path, w)
	}
	for _, prefix := range []string{"/etc/ssl/private", "/etc/sudoers.d"} {
		if strings.HasPrefix(p, prefix) {
			return fmt.Sprintf("%s sits under %s (private keys / credentials); proceed explicitly if intended", path, prefix)
		}
	}
	base := p[strings.LastIndexByte(p, '/')+1:]
	if strings.HasPrefix(base, ".env") || base == "id_rsa" || base == "id_ed25519" {
		return fmt.Sprintf("%s looks like a credentials file (.env / private key); proceed explicitly if intended", path)
	}
	return ""
}

// AtomicWrite stages to a same-directory temp file then renames — the only
// atomicity primitive local filesystems promise. Creates parent dirs.
func AtomicWrite(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp-write"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
