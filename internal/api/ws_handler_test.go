package api

import (
	"testing"
	"time"
)

func TestWSHub_BroadcastToNoClients(t *testing.T) {
	hub := NewWSHub()
	// Broadcast with no clients must not panic.
	hub.Broadcast(WSEvent{
		Type:      "state_transition",
		Payload:   StateTransitionPayload{ExecutionID: "e1", StateName: "S1"},
		Timestamp: time.Now(),
	})
}

func TestWSHub_RegisterUnregister(t *testing.T) {
	hub := NewWSHub()
	if len(hub.clients) != 0 {
		t.Fatal("expected empty hub")
	}
	// Register/Unregister with a nil conn pointer (just tests map ops).
	hub.mu.Lock()
	hub.clients[nil] = struct{}{}
	hub.mu.Unlock()

	hub.Unregister(nil)
	hub.mu.RLock()
	n := len(hub.clients)
	hub.mu.RUnlock()
	if n != 0 {
		t.Fatalf("expected 0 clients after unregister, got %d", n)
	}
}
