package agent

import (
	"context"
	"encoding/json"
	"testing"
)

func TestACPAgentSetModelConfigInvalidatesAllCachedThreads(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{Command: "codex"})
	a.threads["user-a"] = "thread-1"
	a.threads["user-b"] = "thread-2"
	a.sessions["user-a"] = "sess-1"
	a.sessionOwners["sess-1"] = "user-a"

	a.SetModelConfig("gpt-5.1-mini", "low")

	a.mu.Lock()
	_, aStillCached := a.threads["user-a"]
	_, bStillCached := a.threads["user-b"]
	_, sessionStillCached := a.sessions["user-a"]
	gotModel := a.model
	gotEffort := a.modelReasoningEffort
	a.mu.Unlock()

	if aStillCached || bStillCached {
		t.Fatal("SetModelConfig should invalidate every cached thread, not just one conversation, since the change affects all future turns")
	}
	if sessionStillCached {
		t.Fatal("SetModelConfig should also clear the legacy-ACP session cache")
	}
	if gotModel != "gpt-5.1-mini" || gotEffort != "low" {
		t.Fatalf("model/reasoningEffort = %q/%q, want gpt-5.1-mini/low", gotModel, gotEffort)
	}
}

func TestACPAgentSetModelConfigIsNoOpWhenUnchanged(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{Command: "codex", Model: "gpt-5.1-mini", ModelReasoningEffort: "low"})
	a.threads["user-a"] = "thread-1" // simulate a real thread/start having happened since

	a.SetModelConfig("gpt-5.1-mini", "low") // identical to what NewACPAgent already set

	a.mu.Lock()
	_, threadStillCached := a.threads["user-a"]
	a.mu.Unlock()
	if !threadStillCached {
		t.Fatal("setting an identical model config must not invalidate an already-established thread")
	}
}

func TestACPAgentSetModelConfigAppliesToNextThreadStart(t *testing.T) {
	var capturedParams map[string]interface{}
	a := NewACPAgent(ACPAgentConfig{Command: "codex"})
	a.rpcCall = func(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
		if method == "thread/start" {
			b, _ := json.Marshal(params)
			json.Unmarshal(b, &capturedParams)
			return json.RawMessage(`{"thread":{"id":"thread-1"}}`), nil
		}
		return json.RawMessage(`{}`), nil
	}

	a.SetModelConfig("gpt-5.1-mini", "high")

	if _, _, err := a.getOrCreateThread(context.Background(), "user-a"); err != nil {
		t.Fatalf("getOrCreateThread: %v", err)
	}

	if capturedParams["model"] != "gpt-5.1-mini" {
		t.Fatalf("thread/start params.model = %v, want gpt-5.1-mini", capturedParams["model"])
	}
	cfg, ok := capturedParams["config"].(map[string]interface{})
	if !ok || cfg["model_reasoning_effort"] != "high" {
		t.Fatalf("thread/start params.config.model_reasoning_effort = %v, want high", capturedParams["config"])
	}
}
