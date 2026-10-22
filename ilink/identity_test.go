package ilink

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestBaseInfoDefaultsToClientIdentity(t *testing.T) {
	data, err := json.Marshal(GetUpdatesRequest{BaseInfo: BaseInfo{}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"base_info":{"channel_version":"2.4.10","bot_agent":"OpenClaw"}`) {
		t.Fatalf("unexpected body: %s", data)
	}
	data, _ = json.Marshal(&SendMessageRequest{})
	if !strings.Contains(string(data), `"channel_version":"2.4.10"`) {
		t.Fatalf("pointer request lost base_info defaults: %s", data)
	}
}

func TestAppClientVersionEncodesChannelVersion(t *testing.T) {
	want := 0
	for _, part := range strings.Split(ChannelVersion, ".") {
		n, err := strconv.Atoi(part)
		if err != nil {
			t.Fatal(err)
		}
		want = want<<8 | n&0xff
	}
	if AppClientVersion != strconv.Itoa(want) {
		t.Fatalf("AppClientVersion=%s, want %d for %s", AppClientVersion, want, ChannelVersion)
	}
}

func TestMessageIDAcceptsNumberAndString(t *testing.T) {
	cases := map[string]MessageID{
		`{"message_id":7518889480449866248}`:    7518889480449866248,
		`{"message_id":"18446744073709551615"}`: 18446744073709551615,
		`{"message_id":null}`:                   0,
		`{}`:                                    0,
	}
	for body, want := range cases {
		var msg WeixinMessage
		if err := json.Unmarshal([]byte(body), &msg); err != nil {
			t.Fatalf("%s: %v", body, err)
		}
		if msg.MessageID != want {
			t.Fatalf("%s: got %d want %d", body, msg.MessageID, want)
		}
	}
	var msg WeixinMessage
	if err := json.Unmarshal([]byte(`{"message_id":"abc"}`), &msg); err == nil {
		t.Fatal("expected error for non-numeric message_id")
	}
	if MessageID(0).String() != "" || MessageID(18446744073709551615).String() != "18446744073709551615" {
		t.Fatal("unexpected String output")
	}
}

func TestRequestsCarryAppHeaders(t *testing.T) {
	var seen []http.Header
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Clone())
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		w.Write([]byte(`{"ret":0}`))
	}))
	defer srv.Close()

	c := NewClient(&Credentials{BotToken: "t", BaseURL: srv.URL})
	if _, err := c.GetUpdates(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := c.doGet(context.Background(), srv.URL+"/qr", &out); err != nil {
		t.Fatal(err)
	}
	for i, h := range seen {
		if h.Get("iLink-App-Id") != "bot" || h.Get("iLink-App-ClientVersion") != "132106" {
			t.Fatalf("request %d missing app headers: %v", i, h)
		}
	}
	if !strings.Contains(bodies[0], `"channel_version":"2.4.10"`) {
		t.Fatalf("getupdates body: %s", bodies[0])
	}
}
