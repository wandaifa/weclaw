package agent

import "testing"

func TestACPAgentSetPersonaOverrideInvalidatesCachedThread(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{Command: "codex"})
	a.threads["user-a"] = "thread-1"
	a.sessions["user-a"] = "sess-1"
	a.sessionOwners["sess-1"] = "user-a"

	a.SetPersonaOverride("user-a", PersonaOverride{SystemPrompt: "脱敏人格"})

	a.mu.Lock()
	_, threadStillCached := a.threads["user-a"]
	_, sessionStillCached := a.sessions["user-a"]
	got := a.personas["user-a"]
	a.mu.Unlock()

	if threadStillCached {
		t.Fatal("SetPersonaOverride should invalidate the cached thread so the next turn re-does thread/start with the new baseInstructions")
	}
	if sessionStillCached {
		t.Fatal("SetPersonaOverride should also clear the legacy-ACP session cache for the same conversationID")
	}
	if got.SystemPrompt != "脱敏人格" {
		t.Fatalf("stored override = %+v, want SystemPrompt=脱敏人格", got)
	}
}

func TestACPAgentSetPersonaOverrideIsNoOpWhenUnchanged(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{Command: "codex"})
	a.SetPersonaOverride("user-a", PersonaOverride{SystemPrompt: "脱敏人格"})
	a.threads["user-a"] = "thread-1" // simulate a real thread/start having happened since

	a.SetPersonaOverride("user-a", PersonaOverride{SystemPrompt: "脱敏人格"}) // identical override

	a.mu.Lock()
	_, threadStillCached := a.threads["user-a"]
	a.mu.Unlock()
	if !threadStillCached {
		t.Fatal("setting an identical override must not invalidate an already-established thread")
	}
}

func TestACPAgentSetPersonaOverrideIsPerConversation(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{Command: "codex"})
	a.SetPersonaOverride("user-a", PersonaOverride{SystemPrompt: "A"})
	a.SetPersonaOverride("user-b", PersonaOverride{SystemPrompt: "B"})

	a.mu.Lock()
	gotA := a.personas["user-a"]
	gotB := a.personas["user-b"]
	a.mu.Unlock()

	if gotA.SystemPrompt != "A" || gotB.SystemPrompt != "B" {
		t.Fatalf("personas not stored independently: a=%+v b=%+v", gotA, gotB)
	}
}
