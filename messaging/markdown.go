package messaging

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

var (
	// Code blocks: strip fences, keep code content
	reCodeBlock = regexp.MustCompile("(?s)```[^\n]*\n?(.*?)```")
	// Inline code: strip backticks, keep content
	reInlineCode = regexp.MustCompile("`([^`]+)`")
	// Images: remove entirely
	reImage = regexp.MustCompile(`!\[[^\]]*\]\([^)]*\)`)
	// Links: keep display text only
	reLink = regexp.MustCompile(`\[([^\]]+)\]\([^)]*\)`)
	// Table separator rows: remove
	reTableSep = regexp.MustCompile(`(?m)^\|[\s:|\-]+\|$`)
	// Table rows: convert pipe-delimited to space-delimited
	reTableRow = regexp.MustCompile(`(?m)^\|(.+)\|$`)
	// Headers: remove # prefix
	reHeader = regexp.MustCompile(`(?m)^#{1,6}\s+`)
	// Bold: **text** or __text__
	reBold = regexp.MustCompile(`\*\*(.+?)\*\*|__(.+?)__`)
	// Italic: *text* or _text_
	reItalic = regexp.MustCompile(`(?:^|[^*])\*([^*]+)\*(?:[^*]|$)|(?:^|[^_])_([^_]+)_(?:[^_]|$)`)
	// Strikethrough: ~~text~~
	reStrike = regexp.MustCompile(`~~(.+?)~~`)
	// Blockquote: > prefix
	reBlockquote = regexp.MustCompile(`(?m)^>\s?`)
	// Horizontal rule
	reHR = regexp.MustCompile(`(?m)^[-*_]{3,}\s*$`)
	// Unordered list markers: -, *, +
	reUL = regexp.MustCompile(`(?m)^(\s*)[-*+]\s+`)
)

// MarkdownToPlainText converts markdown to readable plain text for WeChat.
func MarkdownToPlainText(text string) string {
	result := text

	// Code blocks: strip fences, keep code content
	result = reCodeBlock.ReplaceAllStringFunc(result, func(match string) string {
		parts := reCodeBlock.FindStringSubmatch(match)
		if len(parts) > 1 {
			return strings.TrimSpace(parts[1])
		}
		return match
	})

	// Images: remove entirely
	result = reImage.ReplaceAllString(result, "")

	// Links: keep display text only
	result = reLink.ReplaceAllString(result, "$1")

	// Table separator rows: remove
	result = reTableSep.ReplaceAllString(result, "")

	// Table rows: pipe-delimited to space-delimited
	result = reTableRow.ReplaceAllStringFunc(result, func(match string) string {
		parts := reTableRow.FindStringSubmatch(match)
		if len(parts) > 1 {
			cells := strings.Split(parts[1], "|")
			for i := range cells {
				cells[i] = strings.TrimSpace(cells[i])
			}
			return strings.Join(cells, "  ")
		}
		return match
	})

	// Headers: remove # prefix
	result = reHeader.ReplaceAllString(result, "")

	// Bold
	result = reBold.ReplaceAllStringFunc(result, func(match string) string {
		parts := reBold.FindStringSubmatch(match)
		if parts[1] != "" {
			return parts[1]
		}
		return parts[2]
	})

	// Strikethrough
	result = reStrike.ReplaceAllString(result, "$1")

	// Blockquote
	result = reBlockquote.ReplaceAllString(result, "")

	// Horizontal rule -> empty line
	result = reHR.ReplaceAllString(result, "")

	// Unordered list: replace markers with "• "
	result = reUL.ReplaceAllString(result, "${1}• ")

	// Inline code: strip backticks (do after code blocks)
	result = reInlineCode.ReplaceAllString(result, "$1")

	// WeChat's ilink bot text renders a single "\n" as a soft break (collapsed
	// to a space); only a blank line ("\n\n") becomes a visible line break —
	// verified on a real device 2026-07-25 with a single-vs-double-newline probe
	// (see ROADMAP.md "最近验证"). Promote every run of newlines to exactly one
	// blank line so intended line breaks (bullet lists, numbered steps, the
	// `claude agents` session list) survive instead of collapsing into a
	// "• a • b • c" wall. This also subsumes the old \n{3,} -> \n\n cleanup.
	result = regexp.MustCompile(`\n+`).ReplaceAllString(result, "\n\n")

	return strings.TrimSpace(result)
}

var (
	reFileLink          = regexp.MustCompile(`\[([^\]]+)\]\(file://[^)]*\)`)
	reMarkdownTableCell = regexp.MustCompile(`^:?-{3,}:?$`)
	reExtraBlankLines   = regexp.MustCompile(`\n{3,}`)
)

// FormatWeChatMarkdown preserves Markdown headings (#/##/###), bold (**...**),
// inline code (`...`), and fenced code blocks (```...```) for WeChat's native
// Markdown renderer, while converting non-clickable local file:// links to
// inline code and promoting single newlines outside code blocks and tables to
// blank lines so WeChat does not collapse consecutive list items.
func FormatWeChatMarkdown(text string) string {
	result := reImage.ReplaceAllString(text, "")
	result = reFileLink.ReplaceAllString(result, "`$1`")

	// Split by fenced code blocks so newlines inside ```...``` stay single "\n".
	var out strings.Builder
	last := 0
	for _, loc := range reCodeBlock.FindAllStringIndex(result, -1) {
		outside := result[last:loc[0]]
		out.WriteString(formatWeChatMarkdownLines(outside))
		out.WriteString(result[loc[0]:loc[1]])
		last = loc[1]
	}
	outside := result[last:]
	out.WriteString(formatWeChatMarkdownLines(outside))
	return strings.TrimSpace(out.String())
}

func formatWeChatMarkdownLines(text string) string {
	lines := strings.Split(text, "\n")
	var out strings.Builder
	inTable := false
	for i, line := range lines {
		if i > 0 {
			if inTable && strings.Contains(line, "|") {
				out.WriteByte('\n')
			} else {
				out.WriteString("\n\n")
				inTable = false
			}
		}
		out.WriteString(line)
		if i+1 < len(lines) && isMarkdownTableHeader(line, lines[i+1]) {
			inTable = true
		}
	}
	return reExtraBlankLines.ReplaceAllString(out.String(), "\n\n")
}

func isMarkdownTableHeader(header, separator string) bool {
	if !strings.Contains(header, "|") || !strings.Contains(separator, "|") {
		return false
	}
	cells := func(line string) []string {
		line = strings.TrimSpace(line)
		line = strings.TrimPrefix(line, "|")
		line = strings.TrimSuffix(line, "|")
		return strings.Split(line, "|")
	}
	headerCells, separatorCells := cells(header), cells(separator)
	if len(headerCells) != len(separatorCells) {
		return false
	}
	for _, cell := range separatorCells {
		if !reMarkdownTableCell.MatchString(strings.TrimSpace(cell)) {
			return false
		}
	}
	return true
}

// WeChatMarkdownChunks splits Markdown text into chunks under limit without
// stripping Markdown syntax.
func WeChatMarkdownChunks(text string, limit int) []string {
	formatted := FormatWeChatMarkdown(text)
	return splitFormattedChunks(formatted, limit)
}

// WeChatTextChunks turns Markdown into readable WeChat text and splits only at
// paragraph or sentence boundaries when one text message would be too long.
// limit is a UTF-8 byte limit, matching the iLink payload limit.
func WeChatTextChunks(text string, limit int) []string {
	plain := MarkdownToPlainText(text)
	return splitFormattedChunks(plain, limit)
}

func splitFormattedChunks(plain string, limit int) []string {
	if limit <= 0 || len(plain) <= limit {
		return []string{plain}
	}
	payloadLimit := limit - 64 // reserve room for 〖N/M〗 and a newline
	if payloadLimit < 100 {
		payloadLimit = 100
	}
	var raw []string
	current := ""
	for _, paragraph := range strings.Split(plain, "\n\n") {
		candidate := paragraph
		if current != "" {
			candidate = current + "\n\n" + paragraph
		}
		if len(candidate) <= payloadLimit {
			current = candidate
			continue
		}
		if current != "" {
			raw = append(raw, current)
			current = ""
		}
		for len(paragraph) > payloadLimit {
			chunk, remaining := splitTextAtByteLimit(paragraph, payloadLimit)
			raw = append(raw, chunk)
			paragraph = remaining
		}
		current = paragraph
	}
	if current != "" {
		raw = append(raw, current)
	}
	if len(raw) == 1 {
		return raw
	}
	chunks := make([]string, len(raw))
	for index, chunk := range raw {
		chunks[index] = fmt.Sprintf("〖%d/%d〗\n%s", index+1, len(raw), chunk)
	}
	return chunks
}

// splitTextAtByteLimit cuts text at a UTF-8 boundary, preferring the last
// Chinese sentence separator in the latter half of the allowed payload.
func splitTextAtByteLimit(text string, limit int) (string, string) {
	cut := limit
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	for index := cut; index > limit/2; {
		runeValue, size := utf8.DecodeLastRuneInString(text[:index])
		if strings.ContainsRune("。！？；，", runeValue) {
			return text[:index], text[index:]
		}
		index -= size
	}
	return text[:cut], text[cut:]
}
