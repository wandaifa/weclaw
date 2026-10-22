package ilink

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDescribeRefShapeHidesContentKeepsIDs(t *testing.T) {
	raw := json.RawMessage(`{"title":"秘密摘要","svr_id":"7518691007733062920",
		"message_item":{"type":1,"msg_id":18446744073709551615,"text_item":{"text":"私密内容"}},
		"partial_text":{"start":"开头","startindex":3}}`)
	got := describeRefShape(raw)
	for _, leak := range []string{"秘密摘要", "私密内容", "开头"} {
		if strings.Contains(got, leak) {
			t.Fatalf("shape leaks content %q: %s", leak, got)
		}
	}
	for _, want := range []string{
		`svr_id:str("7518691007733062920")`,
		`msg_id:num(18446744073709551615)`,
		`type:num(1)`,
		`title:str(len=4)`,
		`text:str(len=4)`,
		`startindex:num(len=1)`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("shape missing %q: %s", want, got)
		}
	}
}

func TestWeixinMessageDecodesRefMsg(t *testing.T) {
	var msg WeixinMessage
	body := `{"message_id":1,"item_list":[{"type":1,"text_item":{"text":"hi"},"ref_msg":{"svr_id":"9"}}]}`
	if err := json.Unmarshal([]byte(body), &msg); err != nil {
		t.Fatal(err)
	}
	if got := describeRefShape(msg.ItemList[0].RefMsg); got != `{svr_id:str("9")}` {
		t.Fatalf("unexpected shape: %s", got)
	}
}

func TestDescribeRefShapeInvalid(t *testing.T) {
	if got := describeRefShape(json.RawMessage(`{bad`)); got != "invalid(len=4)" {
		t.Fatalf("unexpected: %s", got)
	}
}
