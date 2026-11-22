package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/fastclaw-ai/weclaw/ilink"
	"github.com/fastclaw-ai/weclaw/terminal"
)

func TestHandleTerminalResultRequiresLoopbackAndSessionID(t *testing.T) {
	server := NewServer(nil, "")
	server.SetTerminalResultController(func(_ context.Context, _ []*ilink.Client, req terminal.ResultNotification) (string, error) {
		return "sent", nil
	})

	// Non-loopback rejected.
	req := httptest.NewRequest(http.MethodPost, TerminalResultPath, bytes.NewBufferString(`{"session_id":"s1","source":"shell"}`))
	req.RemoteAddr = "192.168.1.50:12345"
	rec := httptest.NewRecorder()
	server.handleTerminalResult(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-loopback status = %d, want 403", rec.Code)
	}

	// Missing session_id rejected.
	req = httptest.NewRequest(http.MethodPost, TerminalResultPath, bytes.NewBufferString(`{"source":"shell"}`))
	req.RemoteAddr = "127.0.0.1:12345"
	rec = httptest.NewRecorder()
	server.handleTerminalResult(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing session_id status = %d, want 400", rec.Code)
	}

	// Valid loopback request forwarded to controller.
	req = httptest.NewRequest(http.MethodPost, TerminalResultPath, bytes.NewBufferString(`{"session_id":"w1t0p0:s1","source":"codex","text":"hi"}`))
	req.RemoteAddr = "127.0.0.1:12345"
	rec = httptest.NewRecorder()
	server.handleTerminalResult(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("valid request status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil || resp["status"] != "sent" {
		t.Fatalf("resp = %+v, err=%v", resp, err)
	}
}
