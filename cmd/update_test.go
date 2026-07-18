package cmd

import "testing"

func TestUpdateUsesForkRepository(t *testing.T) {
	const want = "wandaifa/weclaw"
	if githubRepo != want {
		t.Fatalf("update repository = %q, want %q", githubRepo, want)
	}
}
