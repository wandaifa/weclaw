package messaging

import (
	"fmt"
	"regexp"
	"strconv"
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
	reBackticks         = regexp.MustCompile("`+")
	reATXHeading        = regexp.MustCompile(`^ {0,3}(#{1,6})[ \t]+(\S.*)$`)
	reSectionNumber     = regexp.MustCompile(`^([（(]?[0-9一二三四五六七八九十百零〇]+([.．][0-9]+)*([、.．）)]|[ \t])|第[0-9一二三四五六七八九十百零〇]+[章节篇部分])`)
)

// consume reports whether the line belongs to a fence, including its delimiters.
type markdownFence struct {
	marker byte
	width  int
}

func (f *markdownFence) consume(line string) bool {
	inside := f.width > 0
	body := strings.TrimLeft(line, " ")
	if len(line)-len(body) > 3 || len(body) < 3 || (body[0] != '`' && body[0] != '~') {
		return inside
	}
	width := 1
	for width < len(body) && body[width] == body[0] {
		width++
	}
	if inside {
		if body[0] == f.marker && width >= f.width && strings.TrimSpace(body[width:]) == "" {
			f.width = 0
		}
		return true
	}
	if width < 3 || (body[0] == '`' && strings.ContainsRune(body[width:], '`')) {
		return false
	}
	f.marker, f.width = body[0], width
	return true
}

func mapWeChatInlineText(line string, transform func(string) string) string {
	if !strings.ContainsRune(line, '`') {
		return transform(line)
	}
	runs := reBackticks.FindAllStringIndex(line, -1)
	var out strings.Builder
	start := 0
	for i := 0; i < len(runs); i++ {
		open := runs[i]
		before := line[:open[0]]
		if (len(before)-len(strings.TrimRight(before, "\\")))%2 != 0 {
			continue
		}
		for j := i + 1; j < len(runs); j++ {
			close := runs[j]
			if close[1]-close[0] == open[1]-open[0] {
				out.WriteString(transform(line[start:open[0]]))
				out.WriteString(line[open[0]:close[1]])
				start, i = close[1], j
				break
			}
		}
	}
	return out.String() + transform(line[start:])
}

// WeChat draws all heading levels at much the same size. An unnumbered outline
// gets section numbers below its top level. Keep author-supplied numbering.
func numberWeChatSubheadings(lines []string) {
	type heading struct{ row, level, title int }
	var headings []heading
	var fence markdownFence
	top := 6
	for row, line := range lines {
		if fence.consume(line) || !strings.Contains(line, "#") {
			continue
		}
		m := reATXHeading.FindStringSubmatchIndex(line)
		if m == nil {
			continue
		}
		if reSectionNumber.MatchString(line[m[4]:]) {
			return
		}
		level := m[3] - m[2]
		top = min(top, level)
		headings = append(headings, heading{row, level, m[4]})
	}
	type section struct{ level, children int }
	parents := []section{{level: top}}
	for _, h := range headings {
		if h.level == top {
			parents = []section{{level: top}}
			continue
		}
		for parents[len(parents)-1].level >= h.level {
			parents = parents[:len(parents)-1]
		}
		parents[len(parents)-1].children++
		var parts []string
		for _, p := range parents {
			parts = append(parts, strconv.Itoa(p.children))
		}
		number := strings.Join(parts, ".")
		if len(parts) == 1 {
			number += "."
		}
		line := lines[h.row]
		lines[h.row] = line[:h.title] + number + " " + line[h.title:]
		parents = append(parents, section{level: h.level})
	}
}

// FormatWeChatMarkdown prepares agent and terminal replies for WeChat's native
// renderer while preserving newlines inside fenced code and pipe tables.
func FormatWeChatMarkdown(text string) string {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	numberWeChatSubheadings(lines)
	var out strings.Builder
	var fence markdownFence
	inTable := false
	for i, line := range lines {
		insideFence := fence.width > 0
		fenced := fence.consume(line)
		if !fenced && strings.TrimSpace(line) == "" {
			inTable = false
			continue
		}
		tableRow := !fenced && inTable && strings.Contains(weChatTableRow(line), "|")
		if out.Len() > 0 {
			if insideFence || tableRow {
				out.WriteByte('\n')
			} else {
				out.WriteString("\n\n")
			}
		}
		if fenced {
			out.WriteString(line)
			inTable = false
			continue
		}
		inTable = tableRow || (i+1 < len(lines) && isMarkdownTableHeader(line, lines[i+1]))
		if inTable {
			line = weChatTableRow(line)
		}
		line = mapWeChatInlineText(line, func(prose string) string {
			prose = weChatImageLinks(weChatEmphasis(prose))
			return reFileLink.ReplaceAllString(prose, "`$1`")
		})
		out.WriteString(line)
	}
	return strings.TrimSpace(out.String())
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
	headerCells, separatorCells := cells(weChatTableRow(header)), cells(separator)
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
