package messaging

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestMarkdownSingleNewlinesBecomeBlankLines is the regression test for the
// 2026-07-25 finding: WeChat's ilink bot text collapses a single "\n" into a
// space, so bullet/numbered lists separated by single newlines rendered as a
// "• a • b • c" wall. MarkdownToPlainText must promote every newline run to a
// blank line so each logical line survives as a visible break.
func TestMarkdownSingleNewlinesBecomeBlankLines(t *testing.T) {
	in := "9 个 session（全部 idle）：\n- remotion-video-studio-0d idle\n- aicodeproject-a0 idle\n- wx-clawbot-53 idle"
	want := "9 个 session（全部 idle）：\n\n• remotion-video-studio-0d idle\n\n• aicodeproject-a0 idle\n\n• wx-clawbot-53 idle"
	if got := MarkdownToPlainText(in); got != want {
		t.Fatalf("MarkdownToPlainText() = %q\nwant %q", got, want)
	}
}

// TestMarkdownExistingBlankLinesPreserved locks in that already-double newlines
// (normal paragraph breaks) are not over-expanded, and that runs of 3+ newlines
// still collapse to exactly one blank line (the behavior the old \n{3,} rule
// provided, now subsumed by the \n+ rule).
func TestMarkdownExistingBlankLinesPreserved(t *testing.T) {
	in := "第一段\n\n第二段\n\n\n\n第三段"
	want := "第一段\n\n第二段\n\n第三段"
	if got := MarkdownToPlainText(in); got != want {
		t.Fatalf("MarkdownToPlainText() = %q\nwant %q", got, want)
	}
}

// TestMarkdownNumberedListBreaksSurvive covers ordered lists, which markdown.go
// does not rewrite the markers of (only unordered -,*,+ become •) but which
// suffer the same single-newline collapse and must also come out one-per-line.
func TestMarkdownNumberedListBreaksSurvive(t *testing.T) {
	in := "步骤：\n1. 打开文件\n2. 改第 3 行\n3. 保存"
	want := "步骤：\n\n1. 打开文件\n\n2. 改第 3 行\n\n3. 保存"
	if got := MarkdownToPlainText(in); got != want {
		t.Fatalf("MarkdownToPlainText() = %q\nwant %q", got, want)
	}
}

func TestWeChatTextChunksKeepParagraphsAndStayWithinLimit(t *testing.T) {
	input := "# 进度\n\n" + strings.Repeat("第一段内容。", 400) + "\n\n" + strings.Repeat("第二段内容。", 400)
	chunks := WeChatTextChunks(input, 1800)
	if len(chunks) < 2 {
		t.Fatalf("chunks = %d, want multiple chunks", len(chunks))
	}
	for index, chunk := range chunks {
		if len(chunk) > 1800 || !strings.HasPrefix(chunk, fmt.Sprintf("〖%d/", index+1)) {
			t.Fatalf("chunk %d invalid: %q", index, chunk[:min(len(chunk), 40)])
		}
	}
}

func TestWeChatTextChunksUseUTF8ByteLimit(t *testing.T) {
	withinLimit := strings.Repeat("测", 5333) + "a" // 16,000 UTF-8 bytes
	if chunks := WeChatTextChunks(withinLimit, 16000); len(chunks) != 1 || chunks[0] != withinLimit {
		t.Fatalf("16,000-byte text should stay in one chunk: %#v", chunks)
	}

	overLimit := withinLimit + "b"
	chunks := WeChatTextChunks(overLimit, 16000)
	if len(chunks) < 2 {
		t.Fatalf("16,001-byte text should be split, got %d chunks", len(chunks))
	}
	var rebuilt strings.Builder
	for index, chunk := range chunks {
		if len(chunk) > 16000 || !utf8.ValidString(chunk) {
			t.Fatalf("chunk %d exceeds byte limit or breaks UTF-8", index)
		}
		_, payload, found := strings.Cut(chunk, "\n")
		if !found {
			t.Fatalf("chunk %d has no split marker: %q", index, chunk)
		}
		rebuilt.WriteString(payload)
	}
	if rebuilt.String() != overLimit {
		t.Fatalf("rebuilt text = %q, want original", rebuilt.String())
	}
}
