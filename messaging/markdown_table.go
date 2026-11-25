package messaging

import "strings"

// WeChat splits table cells even at an escaped pipe. Use a fullwidth pipe
// for this literal character, keeping the ASCII column delimiters intact.
func weChatTableRow(line string) string {
	var out strings.Builder
	start, backslashes := 0, 0
	for i := 0; i < len(line); i++ {
		if line[i] == '\\' {
			backslashes++
			continue
		}
		if line[i] == '|' && backslashes%2 == 1 {
			out.WriteString(line[start : i-1])
			out.WriteRune('｜')
			start = i + 1
		}
		backslashes = 0
	}
	if start == 0 {
		return line
	}
	out.WriteString(line[start:])
	return out.String()
}
