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
		id    string
		count int
	}, 4)
	d := New(ctx, 16, 60*time.Millisecond, func(msg *discordgo.MessageCreate, mergedCount int) {
		calls <- struct {
			id    string
			count int
		}{id: msg.ID, count: mergedCount}
	})

	d.Enqueue(&discordgo.MessageCreate{Message: &discordgo.Message{ID: "m1", GuildID: "g1", ChannelID: "c1"}})
	d.Enqueue(&discordgo.MessageCreate{Message: &discordgo.Message{ID: "m2", GuildID: "g1", ChannelID: "c1"}})
	d.Enqueue(&discordgo.MessageCreate{Message: &discordgo.Message{ID: "m3", GuildID: "g1", ChannelID: "c1"}})

	select {
	case got := <-calls:
		if got.id != "m3" {
			t.Fatalf("latest id = %q, want m3", got.id)
		}
		if got.count != 3 {
			t.Fatalf("merged count = %d, want 3", got.count)
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

	d := New(ctx, 16, 40*time.Millisecond, func(msg *discordgo.MessageCreate, mergedCount int) {
		if mergedCount <= 0 {
			t.Errorf("mergedCount = %d, want > 0", mergedCount)
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
