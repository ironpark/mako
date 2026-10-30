package daemon

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestAgentMessagesSurviveReconnectAndRestart(t *testing.T) {
	dir := t.TempDir()
	var store agentMessages
	if err := store.load(dir); err != nil {
		t.Fatal(err)
	}
	first, err := store.send("a", "b", "review this", "/project")
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.send("b", "a", "result", "/project")
	if err != nil || second.ID <= first.ID {
		t.Fatalf("sequence: %+v %v", second, err)
	}
	for range 2 {
		got, err := store.read(t.Context(), "b", 0, 0, 10, 0)
		if err != nil || len(got) != 1 || got[0] != first {
			t.Fatalf("replayed messages: %v %v", got, err)
		}
	}
	var restored agentMessages
	if err := restored.load(dir); err != nil {
		t.Fatal(err)
	}
	got, err := restored.read(t.Context(), "b", 0, 0, 1, 0)
	if err != nil || len(got) != 1 || got[0].ID != first.ID || got[0].Text != first.Text {
		t.Fatalf("restored %v %v", got, err)
	}
	if _, err := restored.read(t.Context(), "b", 0, first.ID, 10, 0); err != nil {
		t.Fatal(err)
	}
	var acknowledged agentMessages
	if err := acknowledged.load(dir); err != nil {
		t.Fatal(err)
	}
	got, err = acknowledged.read(t.Context(), "b", 0, 0, 10, 0)
	if err != nil || len(got) != 0 {
		t.Fatalf("acknowledged messages survived: %v %v", got, err)
	}
	got, err = acknowledged.read(t.Context(), "a", 0, 0, 10, 0)
	if err != nil || len(got) != 1 || got[0].ID != second.ID {
		t.Fatal("acknowledging one inbox changed another")
	}
}

func TestAgentMessagesWaitCancelAndBackpressure(t *testing.T) {
	var store agentMessages
	result := make(chan []agentMessage, 1)
	go func() { msgs, _ := store.read(t.Context(), "b", 0, 0, 10, time.Second); result <- msgs }()
	if _, err := store.send("a", "b", "hello", "/project"); err != nil {
		t.Fatal(err)
	}
	select {
	case msgs := <-result:
		if len(msgs) != 1 || msgs[0].Text != "hello" {
			t.Fatal(msgs)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waiting inbox did not wake")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := store.read(ctx, "empty", 0, 0, 10, time.Minute); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
	for i := 1; i < maxAgentInbox; i++ {
		if _, err := store.send("a", "b", "fill", "/project"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.send("a", "b", "overflow", "/project"); err == nil {
		t.Fatal("full inbox silently dropped a message")
	}
	if _, err := store.read(t.Context(), "b", 0, 1, 10, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := store.send("a", "b", "room again", "/project"); err != nil {
		t.Fatal(err)
	}
}

func TestAgentMessageWriteFailureDoesNotConsumeIDs(t *testing.T) {
	var store agentMessages
	store.path = filepath.Join(t.TempDir(), "missing", "messages.json")
	if _, err := store.send("a", "b", "failed", "/project"); err == nil {
		t.Fatal("missing storage accepted a message")
	}
	if store.data.Next != 0 || len(store.data.Inboxes) != 0 {
		t.Fatal("failed write mutated the mailbox")
	}
}

func TestAgentMessagesConcurrentSendsHaveUniqueIDs(t *testing.T) {
	var store agentMessages
	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() {
			if _, err := store.send("a", "b", "message", "/project"); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	got, err := store.read(t.Context(), "b", 0, 0, 100, 0)
	if err != nil || len(got) != 50 {
		t.Fatalf("messages %d %v", len(got), err)
	}
	for i, msg := range got {
		if msg.ID != uint64(i+1) {
			t.Fatalf("unordered IDs: %v", got)
		}
	}
}
