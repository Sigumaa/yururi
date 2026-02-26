package dispatch

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
)

const (
	defaultQueueSize      = 128
	defaultCoalesceWindow = 1200 * time.Millisecond
)

type Handler func(msg *discordgo.MessageCreate, mergedCount int)

type Dispatcher struct {
	ctx            context.Context
	handler        Handler
	queueSize      int
	coalesceWindow time.Duration

	mu      sync.Mutex
	workers map[string]*worker
}

type worker struct {
	queue chan *discordgo.MessageCreate
}

func New(ctx context.Context, queueSize int, coalesceWindow time.Duration, handler Handler) *Dispatcher {
	if queueSize <= 0 {
		queueSize = defaultQueueSize
	}
	if coalesceWindow <= 0 {
		coalesceWindow = defaultCoalesceWindow
	}
	if handler == nil {
		handler = func(*discordgo.MessageCreate, int) {}
	}
	return &Dispatcher{
		ctx:            ctx,
		handler:        handler,
		queueSize:      queueSize,
		coalesceWindow: coalesceWindow,
		workers:        map[string]*worker{},
	}
}

func (d *Dispatcher) Enqueue(msg *discordgo.MessageCreate) (dropped bool) {
	if msg == nil {
		return false
	}
	w := d.getOrCreateWorker(msg)

	select {
	case <-d.ctx.Done():
		return false
	default:
	}

	select {
	case w.queue <- msg:
		return false
	default:
	}

	// Queue full: drop one oldest item and prefer the newest message.
	select {
	case <-w.queue:
		dropped = true
	default:
	}
	select {
	case w.queue <- msg:
		return dropped
	default:
		return true
	}
}

func (d *Dispatcher) getOrCreateWorker(msg *discordgo.MessageCreate) *worker {
	key := workerKey(msg)

	d.mu.Lock()
	defer d.mu.Unlock()

	if w, ok := d.workers[key]; ok {
		return w
	}

	w := &worker{queue: make(chan *discordgo.MessageCreate, d.queueSize)}
	d.workers[key] = w
	go d.runWorker(w)
	return w
}

func (d *Dispatcher) runWorker(w *worker) {
	for {
		select {
		case <-d.ctx.Done():
			return
		case first := <-w.queue:
			if first == nil {
				continue
			}

			latest := first
			mergedCount := 1
			timer := time.NewTimer(d.coalesceWindow)
		collect:
			for {
				select {
				case <-d.ctx.Done():
					if !timer.Stop() {
						<-timer.C
					}
					return
				case next := <-w.queue:
					if next != nil {
						latest = next
						mergedCount++
					}
				case <-timer.C:
					break collect
				}
			}

			d.handler(latest, mergedCount)
		}
	}
}

func workerKey(msg *discordgo.MessageCreate) string {
	guildID := "noguild"
	channelID := "nochannel"
	if msg != nil {
		if msg.GuildID != "" {
			guildID = msg.GuildID
		}
		if msg.ChannelID != "" {
			channelID = msg.ChannelID
		}
	}
	return fmt.Sprintf("%s:%s", guildID, channelID)
}
