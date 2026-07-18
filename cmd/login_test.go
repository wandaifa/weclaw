package cmd

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/api"
)

func TestReloadAccountsAt(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if r.Method != http.MethodPost || r.URL.Path != api.AccountReloadPath {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	running, err := reloadAccountsAt(context.Background(), strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	if !running || !called {
		t.Fatalf("running = %v, called = %v, want both true", running, called)
	}
}

func TestLocalAPIURLNormalizesWildcardHost(t *testing.T) {
	got, err := localAPIURL("0.0.0.0:18011", api.AccountReloadPath)
	if err != nil {
		t.Fatal(err)
	}
	want := "http://127.0.0.1:18011" + api.AccountReloadPath
	if got != want {
		t.Fatalf("URL = %q, want %q", got, want)
	}
}
