package messaging

import (
	"regexp"
	"strings"
)

// Only split where the author's whitespace already separates the styled spans.
// In particular, underscores in an identifier and arithmetic stars stay literal.
const emphasisProse = `[^*_|\x60\\\[\]<>]`

var reNestedEmphasis = regexp.MustCompile(`\]\(|<[^>]*(?:>|$)|\*\*(` + emphasisProse + `+?)([ \t]+)([*_])(` + emphasisProse + `+?)([*_])([ \t]+` + emphasisProse + `+)?\*\*`)

func weChatEmphasis(line string) string {
	if !strings.Contains(line, "**") {
		return line
	}
	var out strings.Builder
	last := 0
	for scan := 0; scan < len(line); {
		rest := line[scan:]
		m := reNestedEmphasis.FindStringSubmatchIndex(rest)
		if m == nil {
			break
		}
		start, end := scan+m[0], scan+m[1]
		scan = end
		if m[2] < 0 {
			if line[start] == ']' {
				scan = emphasisLinkEnd(line, end)
			}
			continue
		}
		before := line[:start]
		if (len(before)-len(strings.TrimRight(before, "\\")))%2 != 0 {
			continue
		}
		prefix, gap := rest[m[2]:m[3]], rest[m[4]:m[5]]
		inner := rest[m[8]:m[9]]
		if rest[m[6]] != rest[m[10]] || strings.TrimSpace(inner) != inner ||
			strings.TrimSpace(prefix) != prefix || (start > 0 && line[start-1] == '*') ||
			(end < len(line) && line[end] == '*') {
			continue
		}
		var suffix string
		if m[12] >= 0 {
			suffix = rest[m[12]:m[13]]
			body := strings.TrimLeft(suffix, " \t")
			if body == "" || strings.TrimSpace(body) != body {
				continue
			}
			suffix = suffix[:len(suffix)-len(body)] + "**" + body + "**"
		}
		out.WriteString(line[last:start])
		out.WriteString("**" + prefix + "**" + gap + "***" + inner + "***" + suffix)
		last = end
	}
	if last == 0 {
		return line
	}
	out.WriteString(line[last:])
	return out.String()
}

// Skip the entire link destination and optional title, including quoted or
// escaped parentheses, before considering any more emphasis on the same line.
func emphasisLinkEnd(line string, start int) int {
	depth := 1
	var quote byte
	for i := start; i < len(line); i++ {
		c := line[i]
		switch {
		case c == '\\':
			i++
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '<':
			quote = '>'
		case (c == '"' || c == '\'') && i > start && (line[i-1] == ' ' || line[i-1] == '\t'):
			quote = c
		case c == '(':
			depth++
		case c == ')':
			depth--
			if depth == 0 {
				return i + 1
			}
		}
	}
	return len(line)
}
