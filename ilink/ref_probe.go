package ilink

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
)

// refProbeValueKeys are ID, enum and timestamp fields whose values are logged so
// they can be matched against stored messages; every other string/number is
// reduced to its length.
var refProbeValueKeys = map[string]bool{
	"svr_id": true, "msg_id": true, "message_id": true, "type": true,
	"create_time_ms": true, "update_time_ms": true,
}

// logRefMsgProbe logs the structure of quoted messages without logging chat content.
func logRefMsgProbe(botID string, msg WeixinMessage) {
	for i, item := range msg.ItemList {
		if len(item.RefMsg) == 0 {
			continue
		}
		log.Printf("[ref-probe] bot=%s message_id=%d item=%d type=%d shape=%s",
			botID, msg.MessageID, i, item.Type, describeRefShape(item.RefMsg))
	}
}

// describeRefShape renders a JSON value as its key/type/length skeleton.
func describeRefShape(raw json.RawMessage) string {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return fmt.Sprintf("invalid(len=%d)", len(raw))
	}
	return shapeOf("", v)
}

func shapeOf(key string, v any) string {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, k+":"+shapeOf(k, t[k]))
		}
		return "{" + strings.Join(parts, ",") + "}"
	case []any:
		if len(t) == 0 {
			return "[]"
		}
		return fmt.Sprintf("[%d]%s", len(t), shapeOf(key, t[0]))
	case string:
		if refProbeValueKeys[key] {
			return fmt.Sprintf("str(%q)", t)
		}
		return fmt.Sprintf("str(len=%d)", len([]rune(t)))
	case json.Number:
		if refProbeValueKeys[key] {
			return "num(" + t.String() + ")"
		}
		return fmt.Sprintf("num(len=%d)", len(t.String()))
	case bool:
		return "bool"
	case nil:
		return "null"
	default:
		return "unknown"
	}
}
