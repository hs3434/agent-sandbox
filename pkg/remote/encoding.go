package remote

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
