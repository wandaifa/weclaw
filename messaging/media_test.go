package messaging

import (
	"slices"
	"testing"
)

func TestExtractImageURLs(t *testing.T) {
	text := "check ![img](https://example.com/a.png) and ![](https://example.com/b.jpg)"
	urls := ExtractImageURLs(text)
	if len(urls) != 2 {
		t.Fatalf("expected 2 urls, got %d", len(urls))
	}
	if urls[0] != "https://example.com/a.png" {
		t.Errorf("urls[0] = %q", urls[0])
	}
	if urls[1] != "https://example.com/b.jpg" {
		t.Errorf("urls[1] = %q", urls[1])
	}
}

func TestExtractImageURLs_NoImages(t *testing.T) {
	urls := ExtractImageURLs("just plain text")
	if len(urls) != 0 {
		t.Errorf("expected 0 urls, got %d", len(urls))
	}
}

func TestExtractImageURLs_RelativeURL(t *testing.T) {
	text := "![img](./local.png)"
	urls := ExtractImageURLs(text)
	if len(urls) != 0 {
		t.Errorf("expected 0 urls for relative path, got %d", len(urls))
	}
}

func TestImageExamplesAreNotDownloaded(t *testing.T) {
	const example = "![example](https://example.com/example.png)"
	const real = "![photo](https://example.com/photo.png)"
	reply := "````md\n" + example + "\n```\n" + example + "\n````\n" +
		"~~~md\n" + example + "\n~~~\n" +
		"``literal ` " + example + "`` \\" + example + " " + real
	want := []string{"https://example.com/photo.png"}
	if got := ExtractImageURLs(reply); !slices.Equal(got, want) {
		t.Fatalf("ExtractImageURLs() = %q, want %q", got, want)
	}
}

func TestWeChatImageLinks(t *testing.T) {
	const url = "https://example.com/a.png"
	line := "商品 ![外观](" + url + ") ![](" + url + ") ![本地](file:///a.png)"
	want := "商品 [外观](" + url + ") [图片](" + url + ") "
	if got := weChatImageLinks(line); got != want {
		t.Fatalf("weChatImageLinks() = %q, want %q", got, want)
	}
}

func TestImageDestinationsAndTitles(t *testing.T) {
	const pack = "https://example.com/pack(1).png"
	const photo = "https://example.com/p.png"
	line := "![整箱](" + pack + ") ![商品图](" + photo + ` "介绍（第 1 批）")`
	wantURLs := []string{pack, photo}
	if got := ExtractImageURLs(line); !slices.Equal(got, wantURLs) {
		t.Errorf("ExtractImageURLs() = %q, want %q", got, wantURLs)
	}
	want := "[整箱](" + pack + ") [商品图](" + photo + ")"
	if got := weChatImageLinks(line); got != want {
		t.Errorf("weChatImageLinks() = %q, want %q", got, want)
	}
}

func TestFilenameFromURL(t *testing.T) {
	tests := []struct {
		url  string
		want string
	}{
		{"https://example.com/photo.png", "photo.png"},
		{"https://example.com/path/to/report.pdf", "report.pdf"},
		{"https://example.com/file", "file"},
	}
	for _, tt := range tests {
		got := filenameFromURL(tt.url)
		if got != tt.want {
			t.Errorf("filenameFromURL(%q) = %q, want %q", tt.url, got, tt.want)
		}
	}
}

func TestFilenameFromURL_WithQuery(t *testing.T) {
	got := filenameFromURL("https://example.com/photo.png?token=abc")
	if got != "photo.png" {
		t.Errorf("got %q, want %q", got, "photo.png")
	}
}

func TestStripQuery(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"https://example.com/a?b=c", "https://example.com/a"},
		{"https://example.com/a", "https://example.com/a"},
		{"https://example.com/?x=1&y=2", "https://example.com/"},
	}
	for _, tt := range tests {
		got := stripQuery(tt.input)
		if got != tt.want {
			t.Errorf("stripQuery(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
