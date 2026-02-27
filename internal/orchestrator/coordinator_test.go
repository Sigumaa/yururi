package orchestrator

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sigumaa/yururi/internal/codex"
)

func TestCoordinatorReusesSessionWithSteerTurn(t *testing.T) {
	t.Parallel()

	stub := &runtimeStub{
		startThreadResults: []threadResult{
			{threadID: "thread-1"},
		},
		startTurnResults: []turnResult{
			{result: codex.TurnResult{TurnID: "turn-1", Status: "completed"}},
		},
		steerTurnResults: []turnResult{
			{result: codex.TurnResult{TurnID: "turn-2", Status: "completed"}},
		},
	}

	timestamps := []time.Time{
		time.Date(2026, 2, 27, 10, 0, 0, 0, time.UTC),
		time.Date(2026, 2, 27, 10, 1, 0, 0, time.UTC),
	}
	index := 0
	coordinator := New(stub, WithClock(func() time.Time {
		current := timestamps[index]
		index++
		if index >= len(timestamps) {
			index = len(timestamps) - 1
		}
		return current
	}))

	first, err := coordinator.RunMessageTurn(context.Background(), "g1:c1", codex.TurnInput{
		BaseInstructions:      "base",
		DeveloperInstructions: "dev",
		UserPrompt:            "first",
	})
	if err != nil {
		t.Fatalf("first RunMessageTurn() error = %v", err)
	}
	if first.ThreadID != "thread-1" {
		t.Fatalf("first thread id = %q, want thread-1", first.ThreadID)
	}
	if first.TurnID != "turn-1" {
		t.Fatalf("first turn id = %q, want turn-1", first.TurnID)
	}

	second, err := coordinator.RunMessageTurn(context.Background(), "g1:c1", codex.TurnInput{
		BaseInstructions:      "base-ignored",
		DeveloperInstructions: "dev-ignored",
		UserPrompt:            "second",
	})
	if err != nil {
		t.Fatalf("second RunMessageTurn() error = %v", err)
	}
	if second.ThreadID != "thread-1" {
		t.Fatalf("second thread id = %q, want thread-1", second.ThreadID)
	}
	if second.TurnID != "turn-2" {
		t.Fatalf("second turn id = %q, want turn-2", second.TurnID)
	}

	if got := len(stub.startThreadCalls); got != 1 {
		t.Fatalf("startThread calls = %d, want 1", got)
	}
	if got := len(stub.startTurnCalls); got != 1 {
		t.Fatalf("startTurn calls = %d, want 1", got)
	}
	if got := len(stub.steerTurnCalls); got != 1 {
		t.Fatalf("steerTurn calls = %d, want 1", got)
	}
	if got := stub.steerTurnCalls[0].ExpectedTurnID; got != "turn-1" {
		t.Fatalf("steer expected turn id = %q, want turn-1", got)
	}

	session, ok := coordinator.Session("g1:c1")
	if !ok {
		t.Fatal("session not found")
	}
	if session.ThreadID != "thread-1" {
		t.Fatalf("session thread id = %q, want thread-1", session.ThreadID)
	}
	if session.LastTurnID != "turn-2" {
		t.Fatalf("session last turn id = %q, want turn-2", session.LastTurnID)
	}
	if !session.UpdatedAt.Equal(timestamps[1]) {
		t.Fatalf("session updated_at = %s, want %s", session.UpdatedAt, timestamps[1])
	}
}

func TestCoordinatorSteerFallbackToStartTurnSameThread(t *testing.T) {
	t.Parallel()

	stub := &runtimeStub{
		startThreadResults: []threadResult{
			{threadID: "thread-1"},
		},
		startTurnResults: []turnResult{
			{result: codex.TurnResult{TurnID: "turn-1"}},
			{result: codex.TurnResult{TurnID: "turn-2"}},
		},
		steerTurnResults: []turnResult{
			{err: errors.New("expected turn mismatch")},
		},
	}
	coordinator := New(stub)

	if _, err := coordinator.RunMessageTurn(context.Background(), "g1:c1", codex.TurnInput{
		BaseInstructions:      "base",
		DeveloperInstructions: "dev",
		UserPrompt:            "first",
	}); err != nil {
		t.Fatalf("first RunMessageTurn() error = %v", err)
	}

	got, err := coordinator.RunMessageTurn(context.Background(), "g1:c1", codex.TurnInput{
		UserPrompt: "second",
	})
	if err != nil {
		t.Fatalf("second RunMessageTurn() error = %v", err)
	}
	if got.ThreadID != "thread-1" {
		t.Fatalf("thread id = %q, want thread-1", got.ThreadID)
	}
	if got.TurnID != "turn-2" {
		t.Fatalf("turn id = %q, want turn-2", got.TurnID)
	}

	if got := len(stub.startThreadCalls); got != 1 {
		t.Fatalf("startThread calls = %d, want 1", got)
	}
	if got := len(stub.startTurnCalls); got != 2 {
		t.Fatalf("startTurn calls = %d, want 2", got)
	}
	if got := len(stub.steerTurnCalls); got != 1 {
		t.Fatalf("steerTurn calls = %d, want 1", got)
	}
	if got := stub.startTurnCalls[1].ThreadID; got != "thread-1" {
		t.Fatalf("fallback StartTurn thread id = %q, want thread-1", got)
	}
}

func TestCoordinatorSteerFallbackCreatesNewThreadWhenNeeded(t *testing.T) {
	t.Parallel()

	stub := &runtimeStub{
		startThreadResults: []threadResult{
			{threadID: "thread-1"},
			{threadID: "thread-2"},
		},
		startTurnResults: []turnResult{
			{result: codex.TurnResult{TurnID: "turn-1"}},
			{err: errors.New("thread closed")},
			{result: codex.TurnResult{TurnID: "turn-3"}},
		},
		steerTurnResults: []turnResult{
			{err: errors.New("expected turn mismatch")},
		},
	}
	coordinator := New(stub)

	if _, err := coordinator.RunMessageTurn(context.Background(), "g1:c1", codex.TurnInput{
		BaseInstructions:      "base",
		DeveloperInstructions: "dev",
		UserPrompt:            "first",
	}); err != nil {
		t.Fatalf("first RunMessageTurn() error = %v", err)
	}

	got, err := coordinator.RunMessageTurn(context.Background(), "g1:c1", codex.TurnInput{
		BaseInstructions:      "base-2",
		DeveloperInstructions: "dev-2",
		UserPrompt:            "second",
	})
	if err != nil {
		t.Fatalf("second RunMessageTurn() error = %v", err)
	}
	if got.ThreadID != "thread-2" {
		t.Fatalf("thread id = %q, want thread-2", got.ThreadID)
	}
	if got.TurnID != "turn-3" {
		t.Fatalf("turn id = %q, want turn-3", got.TurnID)
	}

	if got := len(stub.startThreadCalls); got != 2 {
		t.Fatalf("startThread calls = %d, want 2", got)
	}
	if got := len(stub.startTurnCalls); got != 3 {
		t.Fatalf("startTurn calls = %d, want 3", got)
	}
	if got := len(stub.steerTurnCalls); got != 1 {
		t.Fatalf("steerTurn calls = %d, want 1", got)
	}
	if got := stub.startTurnCalls[1].ThreadID; got != "thread-1" {
		t.Fatalf("same-thread fallback thread id = %q, want thread-1", got)
	}
	if got := stub.startTurnCalls[2].ThreadID; got != "thread-2" {
		t.Fatalf("new-thread fallback thread id = %q, want thread-2", got)
	}

	session, ok := coordinator.Session("g1:c1")
	if !ok {
		t.Fatal("session not found")
	}
	if session.ThreadID != "thread-2" {
		t.Fatalf("session thread id = %q, want thread-2", session.ThreadID)
	}
	if session.LastTurnID != "turn-3" {
		t.Fatalf("session last turn id = %q, want turn-3", session.LastTurnID)
	}
}

type runtimeStub struct {
	startThreadResults []threadResult
	startTurnResults   []turnResult
	steerTurnResults   []turnResult

	startThreadCalls []codex.TurnInput
	startTurnCalls   []startTurnCall
	steerTurnCalls   []steerTurnCall
}

type startTurnCall struct {
	ThreadID string
	Prompt   string
}

type steerTurnCall struct {
	ThreadID       string
	ExpectedTurnID string
	Prompt         string
}

type threadResult struct {
	threadID string
	err      error
}

type turnResult struct {
	result codex.TurnResult
	err    error
}

func (s *runtimeStub) StartThread(_ context.Context, input codex.TurnInput) (string, error) {
	s.startThreadCalls = append(s.startThreadCalls, input)
	if len(s.startThreadResults) == 0 {
		return "", errors.New("unexpected StartThread call")
	}
	current := s.startThreadResults[0]
	s.startThreadResults = s.startThreadResults[1:]
	return current.threadID, current.err
}

func (s *runtimeStub) StartTurn(_ context.Context, threadID string, prompt string) (codex.TurnResult, error) {
	s.startTurnCalls = append(s.startTurnCalls, startTurnCall{ThreadID: threadID, Prompt: prompt})
	if len(s.startTurnResults) == 0 {
		return codex.TurnResult{}, errors.New("unexpected StartTurn call")
	}
	current := s.startTurnResults[0]
	s.startTurnResults = s.startTurnResults[1:]
	return current.result, current.err
}

func (s *runtimeStub) SteerTurn(_ context.Context, threadID string, expectedTurnID string, prompt string) (codex.TurnResult, error) {
	s.steerTurnCalls = append(s.steerTurnCalls, steerTurnCall{
		ThreadID:       threadID,
		ExpectedTurnID: expectedTurnID,
		Prompt:         prompt,
	})
	if len(s.steerTurnResults) == 0 {
		return codex.TurnResult{}, errors.New("unexpected SteerTurn call")
	}
	current := s.steerTurnResults[0]
	s.steerTurnResults = s.steerTurnResults[1:]
	return current.result, current.err
}
