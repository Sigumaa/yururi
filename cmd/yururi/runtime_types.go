package main

import (
	"context"
	"sync"
	"time"

	"github.com/sigumaa/yururi/internal/codex"
	"github.com/sigumaa/yururi/internal/discordx"
)

type heartbeatRuntime interface {
	RunTurn(ctx context.Context, input codex.TurnInput) (codex.TurnResult, error)
}

type heartbeatWhisperSender interface {
	SendMessage(ctx context.Context, channelID string, content string) (string, error)
}

type heartbeatWhisperHistoryReader interface {
	ReadMessageHistory(ctx context.Context, channelID string, beforeMessageID string, limit int) ([]discordx.Message, error)
}

type timesWhisperState struct {
	mu     sync.Mutex
	lastAt time.Time
}

const (
	maxHeartbeatLogValueLen        = 280
	timesWhisperDedupeHistoryLimit = 20
	autonomyTimesHistoryLimit      = 200
	autonomyTimesHistoryMaxLen     = 160
)
