package container

import (
	"archive/tar"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// decodeText auto-detects BOM encodings and decodes to UTF-8 text with LF
// line endings (inherited semantics: sandbox-mcp file_operations four-level
// decode — BOM → strict UTF-8 → GBK → replace, AD-025).
func decodeText(raw []byte) []byte {
	// BOM-based
	if bytes.HasPrefix(raw, []byte{0xEF, 0xBB, 0xBF}) {
		return normalizeUTF8LF(string(raw[3:]))
	}
	if bytes.HasPrefix(raw, []byte{0xFF, 0xFE}) || bytes.HasPrefix(raw, []byte{0xFE, 0xFF}) {
		return normalizeUTF8LF(decodeUTF16(raw))
	}
	// strict UTF-8
	if utf8.Valid(raw) {
		return normalizeUTF8LF(string(raw))
	}
	// GBK fallback requires a decoder table; P0 uses replace semantics
	// (full GBK table lands with the ssh×windows coding layer, m3b)
	return normalizeUTF8LF(strings.ToValidUTF8(string(raw), "\uFFFD"))
}

func decodeUTF16(raw []byte) string {
	be := bytes.HasPrefix(raw, []byte{0xFE, 0xFF})
	body := raw
	if len(raw) >= 2 && (be || bytes.HasPrefix(raw, []byte{0xFF, 0xFE})) {
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
	runes := utf16Decode(u16)
	return string(runes)
}

func utf16Decode(u []uint16) []rune {
	var out []rune
	for i := 0; i < len(u); i++ {
		c := u[i]
		if c >= 0xD800 && c <= 0xDBFF && i+1 < len(u) && u[i+1] >= 0xDC00 && u[i+1] <= 0xDFFF {
			out = append(out, ((rune(c)-0xD800)<<10|(rune(u[i+1])-0xDC00))+0x10000)
			i++
			continue
		}
		out = append(out, rune(c))
	}
	return out
}

// normalizeUTF8LF strips a leading BOM and converts CRLF/CR to LF
// (canonical on-disk format, AD-025).
func normalizeUTF8LF(text string) []byte {
	text = strings.TrimPrefix(text, "\uFEFF")
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	return []byte(text)
}

// tarDir packs a directory as a docker build context.
func tarDir(dir string) (io.Reader, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		if info.IsDir() {
			hdr.Name += "/"
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			defer f.Close()
			if _, err := io.Copy(tw, f); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return bytes.NewReader(buf.Bytes()), nil
}
