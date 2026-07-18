package cmd

import "testing"

func TestParseLaunchAgentStatus(t *testing.T) {
	output := `gui/501/ai.weclaw.bridge = {
	state = running
	pid = 87573
}`
	state, pid := parseLaunchAgentStatus(output)
	if state != "running" {
		t.Fatalf("state = %q, want running", state)
	}
	if pid != 87573 {
		t.Fatalf("pid = %d, want 87573", pid)
	}
}

func TestParseLaunchAgentStatusWithoutPID(t *testing.T) {
	state, pid := parseLaunchAgentStatus("state = waiting\n")
	if state != "waiting" {
		t.Fatalf("state = %q, want waiting", state)
	}
	if pid != 0 {
		t.Fatalf("pid = %d, want 0", pid)
	}
}
