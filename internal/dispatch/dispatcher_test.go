package dispatch

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
)

func TestDispatcherCoalescesBurst(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	calls := make(chan struct {
		id   string
		meta CallbackMetadata
	}, 4)
	d := New(ctx, 16, 60*time.Millisecond, func(msg *discordgo.MessageCreate, meta CallbackMetadata) {
		calls <- struct {
			id   string
			meta CallbackMetadata
		}{id: msg.ID, meta: meta}
	})

	d.Enqueue(&discordgo.MessageCreate{Message: &discordgo.Message{ID: "m1", GuildID: "g1", ChannelID: "c1"}})
	d.Enqueue(&discordgo.MessageCreate{Message: &discordgo.Message{ID: "m2", GuildID: "g1", ChannelID: "c1"}})
	d.Enqueue(&discordgo.MessageCreate{Message: &discordgo.Message{ID: "m3", GuildID: "g1", ChannelID: "c1"}})

	select {
	case got := <-calls:
		if got.id != "m3" {
			t.Fatalf("latest id = %q, want m3", got.id)
		}
		if got.meta.MergedCount != 3 {
			t.Fatalf("merged count = %d, want 3", got.meta.MergedCount)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("dispatcher callback timeout")
	}

	select {
	case <-calls:
		t.Fatal("unexpected second callback")
	case <-time.After(150 * time.Millisecond):
	}
}

func TestDispatcherSeparatesChannels(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var mu sync.Mutex
	seen := map[string]bool{}
	ch := make(chan struct{}, 2)

	d := New(ctx, 16, 40*time.Millisecond, func(msg *discordgo.MessageCreate, meta CallbackMetadata) {
		if meta.MergedCount <= 0 {
			t.Errorf("mergedCount = %d, want > 0", meta.MergedCount)
			return
		}
		mu.Lock()
		seen[msg.ChannelID] = true
		mu.Unlock()
		ch <- struct{}{}
	})

	d.Enqueue(&discordgo.MessageCreate{Message: &discordgo.Message{ID: "a", GuildID: "g1", ChannelID: "c1"}})
	d.Enqueue(&discordgo.MessageCreate{Message: &discordgo.Message{ID: "b", GuildID: "g1", ChannelID: "c2"}})

	deadline := time.After(2 * time.Second)
	for i := 0; i < 2; i++ {
		select {
		case <-ch:
		case <-deadline:
			t.Fatal("timeout waiting callbacks")
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if !seen["c1"] || !seen["c2"] {
		t.Fatalf("seen channels = %#v, want c1 and c2", seen)
	}
}

func TestDispatcherCallbackMetadata(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	metaCh := make(chan CallbackMetadata, 1)
	d := New(ctx, 16, 50*time.Millisecond, func(msg *discordgo.MessageCreate, meta CallbackMetadata) {
		if msg != nil && msg.ID == "m2" {
			metaCh <- meta
		}
	})

	d.Enqueue(&discordgo.MessageCreate{Message: &discordgo.Message{ID: "m1", GuildID: "g1", ChannelID: "c1"}})
	d.Enqueue(&discordgo.MessageCreate{Message: &discordgo.Message{ID: "m2", GuildID: "g1", ChannelID: "c1"}})

	select {
	case meta := <-metaCh:
		if meta.MergedCount != 2 {
			t.Fatalf("merged count = %d, want 2", meta.MergedCount)
		}
		if meta.QueueWait < 0 {
			t.Fatalf("queue wait = %s, want >= 0", meta.QueueWait)
		}
		if meta.EnqueuedAt.IsZero() {
			t.Fatal("enqueued_at should be set")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("dispatcher callback timeout")
	}
}
