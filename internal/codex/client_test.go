package codex

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func TestNormalizeMethod(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		input  string
		expect string
	}{
		{
			name:   "snake_case",
			input:  "agent_message_delta",
			expect: "agent_message_delta",
		},
		{
			name:   "slash and camelCase",
			input:  "item/agentMessage/delta",
			expect: "item_agent_message_delta",
		},
		{
			name:   "slash",
			input:  "turn/completed",
			expect: "turn_completed",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := normalizeMethod(tc.input)
			if got != tc.expect {
				t.Fatalf("normalizeMethod(%q) = %q, want %q", tc.input, got, tc.expect)
			}
		})
	}
}

func TestIsAgentMessageDeltaMethod(t *testing.T) {
	t.Parallel()

	if !isAgentMessageDeltaMethod(normalizeMethod("agent_message_delta")) {
		t.Fatal("isAgentMessageDeltaMethod(agent_message_delta) = false, want true")
	}
	if !isAgentMessageDeltaMethod(normalizeMethod("item/agentMessage/delta")) {
		t.Fatal("isAgentMessageDeltaMethod(item/agentMessage/delta) = false, want true")
	}
	if isAgentMessageDeltaMethod(normalizeMethod("turn/completed")) {
		t.Fatal("isAgentMessageDeltaMethod(turn/completed) = true, want false")
	}
}

func TestTurnTextAggregatorItemCompletedExtractsAgentMessageText(t *testing.T) {
	t.Parallel()

	aggregator := newTurnTextAggregator()
	aggregator.consume(normalizeMethod("item/completed"), decodeTestNotificationParams(t, `{"item":{"type":"agentMessage","text":"{\"action\":\"noop\"}"}}`))

	if got := aggregator.FinalText(); got != `{"action":"noop"}` {
		t.Fatalf("turnTextAggregator.FinalText() = %q, want %q", got, `{"action":"noop"}`)
	}
}

func TestTurnTextAggregatorItemCompletedIgnoresUserMessage(t *testing.T) {
	t.Parallel()

	aggregator := newTurnTextAggregator()
	aggregator.consume(normalizeMethod("item/completed"), decodeTestNotificationParams(t, `{"item":{"type":"userMessage","text":"{\"action\":\"reply\",\"content\":\"no\"}"}}`))

	if got := aggregator.FinalText(); got != "" {
		t.Fatalf("turnTextAggregator.FinalText() = %q, want empty", got)
	}
}

func TestTurnTextAggregatorAgentMessageDeltaUsesStringOnly(t *testing.T) {
	t.Parallel()

	aggregator := newTurnTextAggregator()
	method := normalizeMethod("item/agentMessage/delta")

	aggregator.consume(method, decodeTestNotificationParams(t, `{"delta":"foo"}`))
	aggregator.consume(method, decodeTestNotificationParams(t, `{"delta":123}`))
	aggregator.consume(method, decodeTestNotificationParams(t, `{"delta":{"text":"ignored"}}`))
	aggregator.consume(method, decodeTestNotificationParams(t, `{"delta":"bar"}`))

	if got := aggregator.FinalText(); got != "foobar" {
		t.Fatalf("turnTextAggregator.FinalText() = %q, want %q", got, "foobar")
	}
}

func TestTurnTextAggregatorFinalTextPrefersAgentMessageAfterTurnCompleted(t *testing.T) {
	t.Parallel()

	aggregator := newTurnTextAggregator()
	aggregator.consume(normalizeMethod("item/agentMessage/delta"), decodeTestNotificationParams(t, `{"delta":"from-delta"}`))
	aggregator.consume(normalizeMethod("item/completed"), decodeTestNotificationParams(t, `{"item":{"type":"agentMessage","text":"from-item"}}`))
	aggregator.consume(normalizeMethod("turn/completed"), decodeTestNotificationParams(t, `{"output":{"text":"from-turn"}}`))

	if !aggregator.Completed() {
		t.Fatal("turnTextAggregator.Completed() = false, want true")
	}
	if got := aggregator.FinalText(); got != "from-item" {
		t.Fatalf("turnTextAggregator.FinalText() = %q, want %q", got, "from-item")
	}
}

func TestThreadStartParams(t *testing.T) {
	t.Parallel()

	params := threadStartParams("dev-instructions")
	if got := params["approvalPolicy"]; got != "never" {
		t.Fatalf("threadStartParams().approvalPolicy = %v, want never", got)
	}
	if got := params["sandbox"]; got != "workspace-write" {
		t.Fatalf("threadStartParams().sandbox = %v, want workspace-write", got)
	}
	if got := params["developerInstructions"]; got != "dev-instructions" {
		t.Fatalf("threadStartParams().developerInstructions = %v, want dev-instructions", got)
	}
}

func TestBuildDeveloperInstructions(t *testing.T) {
	t.Parallel()

	owner := buildDeveloperInstructions(true)
	nonOwner := buildDeveloperInstructions(false)

	if !strings.Contains(owner, "可愛い女子大生メイド") {
		t.Fatalf("buildDeveloperInstructions(true) missing persona: %q", owner)
	}
	if !strings.Contains(owner, `actionは"noop"または"reply"のみ`) {
		t.Fatalf("buildDeveloperInstructions(true) missing action rule: %q", owner)
	}
	if !strings.Contains(nonOwner, `actionは"noop"または"reply"のみ`) {
		t.Fatalf("buildDeveloperInstructions(false) missing action rule: %q", nonOwner)
	}
	if !strings.Contains(owner, "owner_user_idなので少し甘め") {
		t.Fatalf("buildDeveloperInstructions(true) missing owner tone: %q", owner)
	}
	if strings.Contains(nonOwner, "owner_user_idなので少し甘め") {
		t.Fatalf("buildDeveloperInstructions(false) unexpectedly contains owner tone: %q", nonOwner)
	}
	if !strings.Contains(nonOwner, "通常トーン") {
		t.Fatalf("buildDeveloperInstructions(false) missing non-owner tone: %q", nonOwner)
	}
}

func TestParseDecisionOrNoop(t *testing.T) {
	t.Parallel()

	got, err := parseDecisionOrNoop(" \n\t ")
	if err != nil {
		t.Fatalf("parseDecisionOrNoop(empty) error = %v, want nil", err)
	}
	if got != (Decision{Action: "noop"}) {
		t.Fatalf("parseDecisionOrNoop(empty) = %+v, want %+v", got, Decision{Action: "noop"})
	}
}

func TestIsDirectCall(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{
			name:    "hiragana",
			content: "ゆるり、ちょっと来て",
			want:    true,
		},
		{
			name:    "ascii",
			content: "hey yururi",
			want:    true,
		},
		{
			name:    "not direct call",
			content: "こんにちは",
			want:    false,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := isDirectCall(tc.content)
			if got != tc.want {
				t.Fatalf("isDirectCall(%q) = %v, want %v", tc.content, got, tc.want)
			}
		})
	}
}

func TestRunTurnReturnsNoopWhenFinalTextIsEmpty(t *testing.T) {
	t.Setenv("YURURI_MOCK_CODEX_HELPER", "1")

	client := &Client{
		command: os.Args[0],
		args:    []string{"-test.run=^TestMockCodexProcess$", "--", "turn-completed-empty"},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	got, err := client.RunTurn(ctx, TurnInput{
		AuthorID: "user-1",
		Content:  "ping",
		IsOwner:  false,
	})
	if err != nil {
		t.Fatalf("RunTurn() error = %v", err)
	}
	if got != (Decision{Action: "noop"}) {
		t.Fatalf("RunTurn() = %+v, want %+v", got, Decision{Action: "noop"})
	}
}

func TestRunTurnFallbackReplyWhenNoopAndDirectCall(t *testing.T) {
	t.Setenv("YURURI_MOCK_CODEX_HELPER", "1")

	tests := []struct {
		name    string
		content string
		isOwner bool
	}{
		{
			name:    "non-owner",
			content: "ゆるり",
			isOwner: false,
		},
		{
			name:    "owner",
			content: "yururi",
			isOwner: true,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			client := &Client{
				command: os.Args[0],
				args:    []string{"-test.run=^TestMockCodexProcess$", "--", "turn-completed-empty"},
			}

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			got, err := client.RunTurn(ctx, TurnInput{
				AuthorID: "user-1",
				Content:  tc.content,
				IsOwner:  tc.isOwner,
			})
			if err != nil {
				t.Fatalf("RunTurn() error = %v", err)
			}

			want := Decision{
				Action:  "reply",
				Content: fallbackReply(tc.isOwner),
			}
			if got != want {
				t.Fatalf("RunTurn() = %+v, want %+v", got, want)
			}
		})
	}
}

func TestMockCodexProcess(t *testing.T) {
	if os.Getenv("YURURI_MOCK_CODEX_HELPER") != "1" {
		return
	}

	scenario := ""
	for i, arg := range os.Args {
		if arg == "--" && i+1 < len(os.Args) {
			scenario = os.Args[i+1]
			break
		}
	}
	if scenario == "" {
		t.Fatal("mock codex scenario is required")
	}

	dec := json.NewDecoder(os.Stdin)
	dec.UseNumber()
	enc := json.NewEncoder(os.Stdout)

	switch scenario {
	case "turn-completed-empty":
		readMockRequest(t, dec, initRequestID, "initialize")
		writeMockResponse(t, enc, initRequestID, map[string]any{"ok": true})

		readMockNotification(t, dec, "initialized")

		threadReq := readMockRequest(t, dec, threadRequestID, "thread/start")
		threadParams := decodeNotificationParams(threadReq.Params)
		developerInstructions, ok := threadParams["developerInstructions"].(string)
		if !ok || strings.TrimSpace(developerInstructions) == "" {
			t.Fatalf("thread/start params missing developerInstructions: %#v", threadParams)
		}

		writeMockResponse(t, enc, threadRequestID, map[string]any{
			"thread": map[string]any{"id": "thread-1"},
		})

		readMockRequest(t, dec, turnRequestID, "turn/start")
		writeMockResponse(t, enc, turnRequestID, map[string]any{"ok": true})
		writeMockNotification(t, enc, "turn/completed", map[string]any{
			"output": map[string]any{"text": " \n\t "},
		})
	default:
		t.Fatalf("unknown mock codex scenario: %s", scenario)
	}
}

func TestTurnStartParams(t *testing.T) {
	t.Parallel()

	params := turnStartParams("thread-1", "prompt")
	if got := params["threadId"]; got != "thread-1" {
		t.Fatalf("turnStartParams().threadId = %v, want thread-1", got)
	}

	input, ok := params["input"].([]map[string]any)
	if !ok {
		t.Fatalf("turnStartParams().input type = %T, want []map[string]any", params["input"])
	}
	if len(input) != 1 {
		t.Fatalf("turnStartParams().input len = %d, want 1", len(input))
	}
	if input[0]["type"] != "text" {
		t.Fatalf("turnStartParams().input[0].type = %v, want text", input[0]["type"])
	}
	if input[0]["text"] != "prompt" {
		t.Fatalf("turnStartParams().input[0].text = %v, want prompt", input[0]["text"])
	}

	schema, ok := params["outputSchema"].(map[string]any)
	if !ok {
		t.Fatalf("turnStartParams().outputSchema type = %T, want map[string]any", params["outputSchema"])
	}
	oneOf, ok := schema["oneOf"].([]map[string]any)
	if !ok {
		t.Fatalf("turnStartParams().outputSchema.oneOf type = %T, want []map[string]any", schema["oneOf"])
	}
	if len(oneOf) != 2 {
		t.Fatalf("turnStartParams().outputSchema.oneOf len = %d, want 2", len(oneOf))
	}
	if !hasActionConst(oneOf, "noop") {
		t.Fatalf("turnStartParams().outputSchema missing noop action in %#v", oneOf)
	}
	if !hasActionConst(oneOf, "reply") {
		t.Fatalf("turnStartParams().outputSchema missing reply action in %#v", oneOf)
	}
	if !replyRequiresContent(oneOf) {
		t.Fatalf("turnStartParams().outputSchema reply rule must require non-empty content: %#v", oneOf)
	}
}

func TestExtractThreadID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		raw     json.RawMessage
		want    string
		wantErr bool
	}{
		{
			name: "prefer thread.id",
			raw:  json.RawMessage(`{"thread":{"id":"thread-primary"},"threadId":"thread-fallback","thread_id":"thread-fallback2"}`),
			want: "thread-primary",
		},
		{
			name: "fallback threadId",
			raw:  json.RawMessage(`{"threadId":"thread-camel"}`),
			want: "thread-camel",
		},
		{
			name: "fallback thread_id",
			raw:  json.RawMessage(`{"thread_id":"thread-snake"}`),
			want: "thread-snake",
		},
		{
			name: "blank thread.id uses fallback",
			raw:  json.RawMessage(`{"thread":{"id":"  "},"threadId":"thread-camel"}`),
			want: "thread-camel",
		},
		{
			name:    "missing thread id",
			raw:     json.RawMessage(`{"thread":{"name":"x"}}`),
			wantErr: true,
		},
		{
			name:    "wrong thread.id type",
			raw:     json.RawMessage(`{"thread":{"id":123}}`),
			wantErr: true,
		},
		{
			name:    "result is not object",
			raw:     json.RawMessage(`["thread-1"]`),
			wantErr: true,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := extractThreadID(tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("extractThreadID() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("extractThreadID() error = %v", err)
			}
			if got != tc.want {
				t.Fatalf("extractThreadID() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestWithCodexHomeEnv(t *testing.T) {
	t.Parallel()

	base := []string{
		"HOME=/tmp/original-home",
		"CODEX_HOME=/tmp/old-codex-home",
		"PATH=/usr/bin",
	}
	got := withCodexHomeEnv(base, "/tmp/new-codex-home")

	if countEntries(got, "CODEX_HOME=") != 1 {
		t.Fatalf("withCodexHomeEnv() CODEX_HOME entries = %d, want 1", countEntries(got, "CODEX_HOME="))
	}
	if !containsEnv(got, "CODEX_HOME=/tmp/new-codex-home") {
		t.Fatalf("withCodexHomeEnv() missing CODEX_HOME=/tmp/new-codex-home in %v", got)
	}
	if !containsEnv(got, "HOME=/tmp/original-home") {
		t.Fatalf("withCodexHomeEnv() changed HOME unexpectedly: %v", got)
	}
}

func containsEnv(env []string, entry string) bool {
	for _, v := range env {
		if v == entry {
			return true
		}
	}
	return false
}

func countEntries(env []string, prefix string) int {
	count := 0
	for _, v := range env {
		if strings.HasPrefix(v, prefix) {
			count++
		}
	}
	return count
}

func hasActionConst(oneOf []map[string]any, action string) bool {
	for _, candidate := range oneOf {
		properties, ok := candidate["properties"].(map[string]any)
		if !ok {
			continue
		}
		actionProp, ok := properties["action"].(map[string]any)
		if !ok {
			continue
		}
		if actionProp["const"] == action {
			return true
		}
	}
	return false
}

func replyRequiresContent(oneOf []map[string]any) bool {
	for _, candidate := range oneOf {
		properties, ok := candidate["properties"].(map[string]any)
		if !ok {
			continue
		}
		actionProp, ok := properties["action"].(map[string]any)
		if !ok || actionProp["const"] != "reply" {
			continue
		}

		required, ok := candidate["required"].([]string)
		if !ok || !containsString(required, "content") {
			return false
		}
		contentProp, ok := properties["content"].(map[string]any)
		if !ok {
			return false
		}
		if contentProp["type"] != "string" {
			return false
		}
		if contentProp["minLength"] != 1 {
			return false
		}
		return true
	}
	return false
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func decodeTestNotificationParams(t *testing.T, raw string) map[string]any {
	t.Helper()
	return decodeNotificationParams(json.RawMessage(raw))
}

func readMockRequest(t *testing.T, dec *json.Decoder, id int, method string) rpcMessage {
	t.Helper()

	var msg rpcMessage
	if err := dec.Decode(&msg); err != nil {
		t.Fatalf("decode request %s: %v", method, err)
	}
	if msg.Method != method {
		t.Fatalf("request method = %q, want %q", msg.Method, method)
	}
	gotID, err := parseID(msg.ID)
	if err != nil {
		t.Fatalf("request %s parse id: %v", method, err)
	}
	if gotID != id {
		t.Fatalf("request %s id = %d, want %d", method, gotID, id)
	}
	return msg
}

func readMockNotification(t *testing.T, dec *json.Decoder, method string) {
	t.Helper()

	var msg rpcMessage
	if err := dec.Decode(&msg); err != nil {
		t.Fatalf("decode notification %s: %v", method, err)
	}
	if msg.Method != method {
		t.Fatalf("notification method = %q, want %q", msg.Method, method)
	}
	if len(msg.ID) != 0 {
		t.Fatalf("notification %s unexpectedly has id: %s", method, string(msg.ID))
	}
}

func writeMockResponse(t *testing.T, enc *json.Encoder, id int, result any) {
	t.Helper()

	if err := enc.Encode(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"result":  result,
	}); err != nil {
		t.Fatalf("encode response id=%d: %v", id, err)
	}
}

func writeMockNotification(t *testing.T, enc *json.Encoder, method string, params any) {
	t.Helper()

	if err := enc.Encode(map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
	}); err != nil {
		t.Fatalf("encode notification %s: %v", method, err)
	}
}
