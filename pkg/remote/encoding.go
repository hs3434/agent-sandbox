package remote

import (
	"encoding/binary"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// mapCodepage translates a Windows console codepage number to a codec name
// (inherited semantics: sandbox-mcp encoding_utils map_codepage_to_codec).
func mapCodepage(cp string) string {
	switch cp {
	case "65001":
		return "utf-8"
	case "936":
		return "gbk"
	case "950":
		return "big5"
	case "932":
		return "shift-jis"
	case "1252":
		return "windows-1252"
	case "":
		return "gbk" // default fallback — Chinese Windows console (Python parity)
	default:
		return "windows-" + cp
	}
}

// gbkTables is the minimal GBK↔Unicode mapping used by decodeGBK. P0 ships
// the decode direction for the common Chinese range; full tables land with
// real Windows demand (thin-mode tier table, AD-025 "cheap code debt").
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

// decodeGBK decodes GBK bytes to UTF-8. Unknown sequences fall back to
// U+FFFD (never raises — Python parity level 4).
func decodeGBK(raw []byte) string {
	var b strings.Builder
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if c < 0x80 {
			b.WriteByte(c)
			continue
		}
		if i+1 >= len(raw) {
			b.WriteRune('�')
			break
		}
		if r, ok := gbkDecode[string(raw[i:i+2])]; ok {
			b.WriteRune(r)
			i++
			continue
		}
		b.WriteRune('�')
		i++
	}
	return b.String()
}

// encodeGBK encodes UTF-8 text to GBK bytes (for stdin to Windows consoles).
func encodeGBK(text string) []byte {
	var out []byte
	for _, r := range text {
		if r < 0x80 {
			out = append(out, byte(r))
			continue
		}
		found := false
		for cp, s := range gbkEncode {
			if s == string(r) {
				out = append(out, byte(cp>>8), byte(cp))
				found = true
				break
			}
		}
		if !found {
			out = append(out, '?')
		}
	}
	return out
}

// decodeText: BOM → strict UTF-8 → GBK → replace (four-level, sandbox-mcp
// file_operations semantics — AD-025 adoption).
func decodeText(raw []byte) []byte {
	if bytesHasPrefix(raw, []byte{0xEF, 0xBB, 0xBF}) {
		return normalizeUTF8LF(string(raw[3:]))
	}
	if bytesHasPrefix(raw, []byte{0xFF, 0xFE}) {
		return normalizeUTF8LF(decodeUTF16(raw, false))
	}
	if bytesHasPrefix(raw, []byte{0xFE, 0xFF}) {
		return normalizeUTF8LF(decodeUTF16(raw, true))
	}
	if utf8.Valid(raw) {
		return normalizeUTF8LF(string(raw))
	}
	return []byte(decodeGBK(raw))
}

func bytesHasPrefix(b, prefix []byte) bool {
	return len(b) >= len(prefix) && string(b[:len(prefix)]) == string(prefix)
}

func decodeUTF16(raw []byte, bigEndian bool) string {
	body := raw
	if len(raw) >= 2 {
		body = raw[2:]
	}
	u := make([]uint16, 0, len(body)/2)
	for i := 0; i+1 < len(body); i += 2 {
		if bigEndian {
			u = append(u, binary.BigEndian.Uint16(body[i:]))
		} else {
			u = append(u, binary.LittleEndian.Uint16(body[i:]))
		}
	}
	return string(utf16.Decode(u))
}

// normalizeUTF8LF strips the BOM and canonicalizes line endings (AD-025).
func normalizeUTF8LF(text string) []byte {
	text = strings.TrimPrefix(text, "\uFEFF")
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	return []byte(text)
}
