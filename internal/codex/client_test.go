package codex

import (
	"encoding/json"
	"strings"
	"testing"
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

	params := threadStartParams()
	if got := params["approvalPolicy"]; got != "never" {
		t.Fatalf("threadStartParams().approvalPolicy = %v, want never", got)
	}
	if got := params["sandbox"]; got != "workspace-write" {
		t.Fatalf("threadStartParams().sandbox = %v, want workspace-write", got)
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
