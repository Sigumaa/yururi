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

type Handler func(msg *discordgo.MessageCreate, meta CallbackMetadata)

type CallbackMetadata struct {
	MergedCount int           `json:"merged_count"`
	QueueWait   time.Duration `json:"queue_wait_ms"`
	EnqueuedAt  time.Time     `json:"enqueued_at"`
}

type Dispatcher struct {
	ctx            context.Context
	handler        Handler
	queueSize      int
	coalesceWindow time.Duration

	mu      sync.Mutex
	workers map[string]*worker
}

type worker struct {
	queue chan queuedMessage
}

type queuedMessage struct {
	msg        *discordgo.MessageCreate
	enqueuedAt time.Time
}

func New(ctx context.Context, queueSize int, coalesceWindow time.Duration, handler Handler) *Dispatcher {
	if queueSize <= 0 {
		queueSize = defaultQueueSize
	}
	if coalesceWindow <= 0 {
		coalesceWindow = defaultCoalesceWindow
	}
	if handler == nil {
		handler = func(*discordgo.MessageCreate, CallbackMetadata) {}
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
	case w.queue <- queuedMessage{msg: msg, enqueuedAt: time.Now()}:
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
	case w.queue <- queuedMessage{msg: msg, enqueuedAt: time.Now()}:
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

	w := &worker{queue: make(chan queuedMessage, d.queueSize)}
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
			if first.msg == nil {
				continue
			}

			latest := first.msg
			mergedCount := 1
			enqueuedAt := first.enqueuedAt
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
					if next.msg != nil {
						latest = next.msg
						mergedCount++
					}
				case <-timer.C:
					break collect
				}
			}

			queueWait := time.Since(enqueuedAt)
			if queueWait < 0 {
				queueWait = 0
			}
			d.handler(latest, CallbackMetadata{
				MergedCount: mergedCount,
				QueueWait:   queueWait,
				EnqueuedAt:  enqueuedAt,
			})
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
