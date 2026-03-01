package main

import (
	"context"

	"github.com/sigumaa/yururi/internal/codex"
)

type heartbeatRuntime interface {
	RunTurn(ctx context.Context, input codex.TurnInput) (codex.TurnResult, error)
}

const (
	maxHeartbeatLogValueLen = 280
)
