package cmd

import (
	"context"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/ilink"
	"github.com/fastclaw-ai/weclaw/messaging"
)

func TestAccountManagerReloadAddsDeduplicatesAndReplaces(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan string, 4)
	stopped := make(chan string, 4)
	runner := func(ctx context.Context, creds *ilink.Credentials, _ *messaging.Handler, _ func(string)) {
		started <- creds.ILinkBotID + ":" + creds.BotToken
		<-ctx.Done()
		stopped <- creds.ILinkBotID + ":" + creds.BotToken
	}
	manager := newAccountManagerWithRunner(ctx, nil, runner)

	first := &ilink.Credentials{ILinkBotID: "bot-1", BotToken: "token-1"}
	result, err := manager.Reload([]*ilink.Credentials{first})
	if err != nil {
		t.Fatal(err)
	}
	if result.Added != 1 || result.Replaced != 0 || len(result.Clients) != 1 {
		t.Fatalf("initial reload = %+v, want one added client", result)
	}
	expectChannelValue(t, started, "bot-1:token-1")

	result, err = manager.Reload([]*ilink.Credentials{first})
	if err != nil {
		t.Fatal(err)
	}
	if result.Added != 0 || result.Replaced != 0 || len(result.Clients) != 1 {
		t.Fatalf("duplicate reload = %+v, want no changes", result)
	}
	select {
	case value := <-started:
		t.Fatalf("duplicate reload started %q", value)
	case <-time.After(30 * time.Millisecond):
	}

	replacement := &ilink.Credentials{ILinkBotID: "bot-1", BotToken: "token-2"}
	second := &ilink.Credentials{ILinkBotID: "bot-2", BotToken: "token-3"}
	result, err = manager.Reload([]*ilink.Credentials{replacement, second})
	if err != nil {
		t.Fatal(err)
	}
	if result.Added != 1 || result.Replaced != 1 || len(result.Clients) != 2 {
		t.Fatalf("changed reload = %+v, want one added and one replaced", result)
	}
	expectChannelValue(t, stopped, "bot-1:token-1")

	gotStarts := map[string]bool{
		receiveChannelValue(t, started): true,
		receiveChannelValue(t, started): true,
	}
	if !gotStarts["bot-1:token-2"] || !gotStarts["bot-2:token-3"] {
		t.Fatalf("unexpected monitor starts: %v", gotStarts)
	}

	cancel()
	manager.Wait()
}

func TestAccountManagerReloadRemovesDisabledAccount(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan string, 1)
	runner := func(ctx context.Context, creds *ilink.Credentials, _ *messaging.Handler, _ func(string)) {
		<-ctx.Done()
		stopped <- creds.ILinkBotID
	}
	manager := newAccountManagerWithRunner(ctx, nil, runner)
	first := &ilink.Credentials{ILinkBotID: "bot-1", BotToken: "token-1"}
	second := &ilink.Credentials{ILinkBotID: "bot-2", BotToken: "token-2"}
	if _, err := manager.Reload([]*ilink.Credentials{first, second}); err != nil {
		t.Fatal(err)
	}

	result, err := manager.Reload([]*ilink.Credentials{second})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Clients) != 1 || result.Clients[0].BotID() != "bot-2" {
		t.Fatalf("clients = %+v, want only bot-2", result.Clients)
	}
	expectChannelValue(t, stopped, "bot-1")
	cancel()
	manager.Wait()
}

func expectChannelValue(t *testing.T, ch <-chan string, want string) {
	t.Helper()
	if got := receiveChannelValue(t, ch); got != want {
		t.Fatalf("channel value = %q, want %q", got, want)
	}
}

func receiveChannelValue(t *testing.T, ch <-chan string) string {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for channel value")
		return ""
	}
}
