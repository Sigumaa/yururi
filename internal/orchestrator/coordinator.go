package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/sigumaa/yururi/internal/codex"
)

type SessionState struct {
	ThreadID   string
	LastTurnID string
	UpdatedAt  time.Time
}

type Runtime interface {
	StartThread(ctx context.Context, input codex.TurnInput) (string, error)
	StartTurn(ctx context.Context, threadID string, prompt string) (codex.TurnResult, error)
	SteerTurn(ctx context.Context, threadID string, expectedTurnID string, prompt string) (codex.TurnResult, error)
}

type Coordinator struct {
	runtime Runtime
	now     func() time.Time

	mu       sync.Mutex
	sessions map[string]SessionState
}

type Option func(*Coordinator)

func WithClock(now func() time.Time) Option {
	return func(c *Coordinator) {
		if now != nil {
			c.now = now
		}
	}
}

func New(runtime Runtime, opts ...Option) *Coordinator {
	c := &Coordinator{
		runtime:  runtime,
		now:      time.Now,
		sessions: map[string]SessionState{},
	}
	for _, opt := range opts {
		if opt != nil {
			opt(c)
		}
	}
	return c
}

func (c *Coordinator) RunMessageTurn(ctx context.Context, channelKey string, input codex.TurnInput) (codex.TurnResult, error) {
	key := strings.TrimSpace(channelKey)
	if key == "" {
		return codex.TurnResult{}, errors.New("channel key is required")
	}
	if c.runtime == nil {
		return codex.TurnResult{}, errors.New("runtime is required")
	}

	session, hasSession := c.session(key)
	if !hasSession || strings.TrimSpace(session.ThreadID) == "" {
		return c.startNewThreadTurn(ctx, key, input)
	}

	threadID := strings.TrimSpace(session.ThreadID)
	lastTurnID := strings.TrimSpace(session.LastTurnID)
	if lastTurnID == "" {
		result, err := c.runtime.StartTurn(ctx, threadID, input.UserPrompt)
		if err == nil {
			c.storeSession(key, withThreadFallback(result, threadID))
			return withThreadFallback(result, threadID), nil
		}

		fallback, fallbackErr := c.startNewThreadTurn(ctx, key, input)
		if fallbackErr != nil {
			return codex.TurnResult{}, fmt.Errorf("start turn in existing thread failed: %w", errors.Join(err, fallbackErr))
		}
		return fallback, nil
	}

	steerResult, steerErr := c.runtime.SteerTurn(ctx, threadID, lastTurnID, input.UserPrompt)
	if steerErr == nil {
		result := withThreadFallback(steerResult, threadID)
		c.storeSession(key, result)
		return result, nil
	}

	startResult, startErr := c.runtime.StartTurn(ctx, threadID, input.UserPrompt)
	if startErr == nil {
		result := withThreadFallback(startResult, threadID)
		c.storeSession(key, result)
		return result, nil
	}

	fallbackResult, fallbackErr := c.startNewThreadTurn(ctx, key, input)
	if fallbackErr != nil {
		return codex.TurnResult{}, fmt.Errorf("turn recovery failed: %w", errors.Join(steerErr, startErr, fallbackErr))
	}
	return fallbackResult, nil
}

func (c *Coordinator) Session(channelKey string) (SessionState, bool) {
	key := strings.TrimSpace(channelKey)
	if key == "" {
		return SessionState{}, false
	}
	return c.session(key)
}

func (c *Coordinator) ResetSession(channelKey string) bool {
	key := strings.TrimSpace(channelKey)
	if key == "" {
		return false
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if _, ok := c.sessions[key]; !ok {
		return false
	}
	delete(c.sessions, key)
	return true
}

func ChannelKey(guildID string, channelID string) string {
	guildID = strings.TrimSpace(guildID)
	channelID = strings.TrimSpace(channelID)
	if guildID == "" {
		guildID = "noguild"
	}
	if channelID == "" {
		channelID = "nochannel"
	}
	return guildID + ":" + channelID
}

func (c *Coordinator) startNewThreadTurn(ctx context.Context, channelKey string, input codex.TurnInput) (codex.TurnResult, error) {
	threadID, err := c.runtime.StartThread(ctx, input)
	if err != nil {
		return codex.TurnResult{}, err
	}

	result, err := c.runtime.StartTurn(ctx, threadID, input.UserPrompt)
	if err != nil {
		return codex.TurnResult{}, err
	}
	result = withThreadFallback(result, threadID)
	c.storeSession(channelKey, result)
	return result, nil
}

func (c *Coordinator) storeSession(channelKey string, result codex.TurnResult) {
	threadID := strings.TrimSpace(result.ThreadID)
	lastTurnID := strings.TrimSpace(result.TurnID)
	if threadID == "" && lastTurnID == "" {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	prev := c.sessions[channelKey]
	if threadID == "" {
		threadID = prev.ThreadID
	}
	if lastTurnID == "" {
		lastTurnID = prev.LastTurnID
	}

	c.sessions[channelKey] = SessionState{
		ThreadID:   threadID,
		LastTurnID: lastTurnID,
		UpdatedAt:  c.now().UTC(),
	}
}

func (c *Coordinator) session(channelKey string) (SessionState, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	session, ok := c.sessions[channelKey]
	return session, ok
}

func withThreadFallback(result codex.TurnResult, fallbackThreadID string) codex.TurnResult {
	if strings.TrimSpace(result.ThreadID) == "" {
		result.ThreadID = strings.TrimSpace(fallbackThreadID)
	}
	return result
}
