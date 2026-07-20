package ilink

import "testing"

func TestSessionExpiredErrorIsDistinct(t *testing.T) {
	if ErrSessionExpired == nil {
		t.Fatal("ErrSessionExpired must identify an unrecoverable session")
	}
}
