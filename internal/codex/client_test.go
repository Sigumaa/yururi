package codex

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sigumaa/yururi/internal/config"
)

func TestNormalizeMethod(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		input  string
		expect string
	}{
		{name: "snake_case", input: "agent_message_delta", expect: "agent_message_delta"},
		{name: "slash and camelCase", input: "item/agentMessage/delta", expect: "item_agent_message_delta"},
		{name: "slash", input: "turn/completed", expect: "turn_completed"},
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

func TestThreadStartParamsIncludesMCPConfig(t *testing.T) {
	t.Parallel()

	input := TurnInput{
		BaseInstructions:      "base",
		DeveloperInstructions: "dev",
	}
	params := threadStartParams(input, "gpt-5.3-codex", "/tmp/work", "medium", "http://127.0.0.1:39393/mcp", nil)

	configValue, ok := params["config"].(map[string]any)
	if !ok {
		t.Fatalf("threadStartParams config missing or invalid: %#v", params)
	}
	mcpServers, ok := configValue["mcp_servers"].(map[string]any)
	if !ok {
		t.Fatalf("threadStartParams config.mcp_servers missing: %#v", configValue)
	}
	discord, ok := mcpServers["discord"].(map[string]any)
	if !ok {
		t.Fatalf("threadStartParams config.mcp_servers.discord missing: %#v", mcpServers)
	}
	if url, _ := discord["url"].(string); url != "http://127.0.0.1:39393/mcp" {
		t.Fatalf("mcp url = %q, want %q", url, "http://127.0.0.1:39393/mcp")
	}

	if got, _ := configValue["model_reasoning_effort"].(string); got != "medium" {
		t.Fatalf("model_reasoning_effort = %q, want %q", got, "medium")
	}
}

func TestThreadStartParamsIncludesExtraMCPServers(t *testing.T) {
	t.Parallel()

	params := threadStartParams(TurnInput{}, "", "", "", "http://127.0.0.1:39393/mcp", map[string]config.CodexMCPServerConfig{
		"twilog-mcp": {
			Command: "npx",
			Args:    []string{"mcp-remote", "https://twilog-mcp.togetter.dev/mcp"},
			Headers: map[string]string{"Authorization": "Bearer token"},
		},
	})

	configValue, ok := params["config"].(map[string]any)
	if !ok {
		t.Fatalf("threadStartParams config missing: %#v", params)
	}
	mcpServers, ok := configValue["mcp_servers"].(map[string]any)
	if !ok {
		t.Fatalf("mcp_servers missing: %#v", configValue)
	}
	twilog, ok := mcpServers["twilog-mcp"].(map[string]any)
	if !ok {
		t.Fatalf("twilog-mcp config missing: %#v", mcpServers)
	}
	if twilog["command"] != "npx" {
		t.Fatalf("twilog command = %#v, want npx", twilog["command"])
	}
	args, ok := twilog["args"].([]string)
	if !ok || len(args) != 2 {
		t.Fatalf("twilog args = %#v, want 2 entries", twilog["args"])
	}
	headers, ok := twilog["headers"].(map[string]any)
	if !ok {
		t.Fatalf("twilog headers = %#v", twilog["headers"])
	}
	if headers["Authorization"] != "Bearer token" {
		t.Fatalf("Authorization header = %#v, want Bearer token", headers["Authorization"])
	}
}

func TestRunTurnReturnsAssistantText(t *testing.T) {
	t.Setenv("YURURI_MOCK_CODEX_HELPER", "1")
	workspaceDir := t.TempDir()
	homeDir := t.TempDir()

	client := NewClient(config.CodexConfig{
		Command:         os.Args[0],
		Args:            []string{"-test.run=^TestMockCodexProcess$", "--", "assistant-text"},
		Model:           "gpt-5.3-codex",
		ReasoningEffort: "medium",
		WorkspaceDir:    workspaceDir,
		HomeDir:         homeDir,
	}, "http://127.0.0.1:39393/mcp")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	got, err := client.RunTurn(ctx, TurnInput{
		BaseInstructions:      "base",
		DeveloperInstructions: "dev",
		UserPrompt:            "ゆるり、見えてる？",
	})
	if err != nil {
		t.Fatalf("RunTurn() error = %v", err)
	}
	if got.Status != "completed" {
		t.Fatalf("RunTurn() status = %q, want completed", got.Status)
	}
	if got.AssistantText != "こんにちは、見えてるよ。" {
		t.Fatalf("RunTurn() assistant text = %q, want %q", got.AssistantText, "こんにちは、見えてるよ。")
	}
	if got.ThreadID != "thread-1" {
		t.Fatalf("RunTurn() thread id = %q, want thread-1", got.ThreadID)
	}
	if got.TurnID != "turn-1" {
		t.Fatalf("RunTurn() turn id = %q, want turn-1", got.TurnID)
	}
}

func TestRunTurnHandlesUserInputRequest(t *testing.T) {
	t.Setenv("YURURI_MOCK_CODEX_HELPER", "1")
	workspaceDir := t.TempDir()
	homeDir := t.TempDir()

	client := NewClient(config.CodexConfig{
		Command:         os.Args[0],
		Args:            []string{"-test.run=^TestMockCodexProcess$", "--", "user-input-request"},
		Model:           "gpt-5.3-codex",
		ReasoningEffort: "medium",
		WorkspaceDir:    workspaceDir,
		HomeDir:         homeDir,
	}, "http://127.0.0.1:39393/mcp")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	got, err := client.RunTurn(ctx, TurnInput{
		BaseInstructions:      "base",
		DeveloperInstructions: "dev",
		UserPrompt:            "test",
	})
	if err != nil {
		t.Fatalf("RunTurn() error = %v", err)
	}
	if got.Status != "completed" {
		t.Fatalf("RunTurn() status = %q, want completed", got.Status)
	}
}

func TestTurnSteerParamsIncludesExpectedTurnID(t *testing.T) {
	t.Parallel()

	params := turnSteerParams("thread-1", "turn-1", "follow up")
	if params["threadId"] != "thread-1" {
		t.Fatalf("turnSteerParams threadId = %#v, want thread-1", params["threadId"])
	}
	if params["expectedTurnId"] != "turn-1" {
		t.Fatalf("turnSteerParams expectedTurnId = %#v, want turn-1", params["expectedTurnId"])
	}
	input, ok := params["input"].([]map[string]any)
	if !ok || len(input) != 1 {
		t.Fatalf("turnSteerParams input = %#v, want single text input", params["input"])
	}
	if input[0]["type"] != "text" {
		t.Fatalf("turnSteerParams input[0].type = %#v, want text", input[0]["type"])
	}
	if input[0]["text"] != "follow up" {
		t.Fatalf("turnSteerParams input[0].text = %#v, want %q", input[0]["text"], "follow up")
	}
}

func TestStartThreadStartTurnAndSteerTurn(t *testing.T) {
	t.Setenv("YURURI_MOCK_CODEX_HELPER", "1")
	workspaceDir := t.TempDir()
	homeDir := t.TempDir()

	client := NewClient(config.CodexConfig{
		Command:         os.Args[0],
		Args:            []string{"-test.run=^TestMockCodexProcess$", "--", "split-start-steer"},
		Model:           "gpt-5.3-codex",
		ReasoningEffort: "medium",
		WorkspaceDir:    workspaceDir,
		HomeDir:         homeDir,
	}, "http://127.0.0.1:39393/mcp")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	threadID, err := client.StartThread(ctx, TurnInput{
		BaseInstructions:      "base",
		DeveloperInstructions: "dev",
	})
	if err != nil {
		t.Fatalf("StartThread() error = %v", err)
	}
	if threadID != "thread-1" {
		t.Fatalf("StartThread() thread id = %q, want thread-1", threadID)
	}

	startResult, err := client.StartTurn(ctx, threadID, "start prompt")
	if err != nil {
		t.Fatalf("StartTurn() error = %v", err)
	}
	if startResult.Status != "completed" {
		t.Fatalf("StartTurn() status = %q, want completed", startResult.Status)
	}
	if startResult.ThreadID != "thread-1" {
		t.Fatalf("StartTurn() thread id = %q, want thread-1", startResult.ThreadID)
	}
	if startResult.TurnID != "turn-1" {
		t.Fatalf("StartTurn() turn id = %q, want turn-1", startResult.TurnID)
	}
	if startResult.AssistantText != "start reply" {
		t.Fatalf("StartTurn() assistant text = %q, want %q", startResult.AssistantText, "start reply")
	}
	if len(startResult.ToolCalls) != 1 {
		t.Fatalf("StartTurn() tool calls = %d, want 1", len(startResult.ToolCalls))
	}
	if startResult.ToolCalls[0].Server != "discord" {
		t.Fatalf("StartTurn() tool server = %q, want discord", startResult.ToolCalls[0].Server)
	}
	if startResult.ToolCalls[0].Tool != "read_message_history" {
		t.Fatalf("StartTurn() tool name = %q, want read_message_history", startResult.ToolCalls[0].Tool)
	}
	if startResult.ToolCalls[0].Status != "completed" {
		t.Fatalf("StartTurn() tool status = %q, want completed", startResult.ToolCalls[0].Status)
	}

	steerResult, err := client.SteerTurn(ctx, threadID, startResult.TurnID, "steer prompt")
	if err != nil {
		t.Fatalf("SteerTurn() error = %v", err)
	}
	if steerResult.Status != "completed" {
		t.Fatalf("SteerTurn() status = %q, want completed", steerResult.Status)
	}
	if steerResult.ThreadID != "thread-1" {
		t.Fatalf("SteerTurn() thread id = %q, want thread-1", steerResult.ThreadID)
	}
	if steerResult.TurnID != "turn-2" {
		t.Fatalf("SteerTurn() turn id = %q, want turn-2", steerResult.TurnID)
	}
	if steerResult.AssistantText != "steer reply" {
		t.Fatalf("SteerTurn() assistant text = %q, want %q", steerResult.AssistantText, "steer reply")
	}
	if len(steerResult.ToolCalls) != 0 {
		t.Fatalf("SteerTurn() tool calls = %d, want 0", len(steerResult.ToolCalls))
	}
}

func TestExtractThreadIDSupportsString(t *testing.T) {
	t.Parallel()

	got, err := extractThreadID(json.RawMessage(`"019c9a9f-1f64-72f1-be59-4abdd8ff88ef"`))
	if err != nil {
		t.Fatalf("extractThreadID() error = %v", err)
	}
	if got != "019c9a9f-1f64-72f1-be59-4abdd8ff88ef" {
		t.Fatalf("extractThreadID() = %q", got)
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

	readMockRequest(t, dec, initRequestID, "initialize")
	writeMockResponse(t, enc, initRequestID, map[string]any{"ok": true})

	readMockNotification(t, dec, "initialized")

	switch scenario {
	case "assistant-text":
		runMockAssistantTextScenario(t, dec, enc)
	case "user-input-request":
		runMockUserInputRequestScenario(t, dec, enc)
	case "split-start-steer":
		runMockSplitStartSteerScenario(t, dec, enc)
	default:
		t.Fatalf("unknown mock codex scenario: %s", scenario)
	}
}

func runMockAssistantTextScenario(t *testing.T, dec *json.Decoder, enc *json.Encoder) {
	t.Helper()

	expectMockThreadStart(t, dec, enc)
	expectMockTurnStart(t, dec, enc, turnRequestID, "thread-1", "ゆるり、見えてる？", "turn-1")

	writeMockNotification(t, enc, "item/completed", map[string]any{
		"item": map[string]any{"type": "agentMessage", "text": "こんにちは、見えてるよ。"},
	})
	writeMockNotification(t, enc, "turn/completed", map[string]any{
		"turn": map[string]any{"id": "turn-1", "status": "completed"},
	})
}

func runMockUserInputRequestScenario(t *testing.T, dec *json.Decoder, enc *json.Encoder) {
	t.Helper()

	expectMockThreadStart(t, dec, enc)
	expectMockTurnStart(t, dec, enc, turnRequestID, "thread-1", "test", "turn-1")

	writeMockRequestFromServer(t, enc, json.RawMessage(`60`), "item/tool/requestUserInput", map[string]any{
		"questions": []map[string]any{{
			"id":      "q1",
			"options": []map[string]any{{"label": "Decline"}, {"label": "Accept"}},
		}},
	})

	resp := readRawMessage(t, dec)
	if strings.TrimSpace(string(resp.ID)) != "60" {
		t.Fatalf("request response id = %s, want 60", string(resp.ID))
	}
	result := decodeNotificationParams(resp.Result)
	answersRaw, ok := result["answers"].(map[string]any)
	if !ok {
		t.Fatalf("request response answers missing: %#v", result)
	}
	q1Raw, ok := answersRaw["q1"].(map[string]any)
	if !ok {
		t.Fatalf("request response answers.q1 missing: %#v", answersRaw)
	}
	answerList, ok := q1Raw["answers"].([]any)
	if !ok || len(answerList) == 0 {
		t.Fatalf("request response answers list missing: %#v", q1Raw)
	}
	if answerList[0] != "Decline" {
		t.Fatalf("request response answer = %#v, want Decline", answerList[0])
	}

	writeMockNotification(t, enc, "turn/completed", map[string]any{
		"turn": map[string]any{"id": "turn-1", "status": "completed"},
	})
}

func runMockSplitStartSteerScenario(t *testing.T, dec *json.Decoder, enc *json.Encoder) {
	t.Helper()

	expectMockThreadStart(t, dec, enc)
	expectMockTurnStart(t, dec, enc, turnRequestID, "thread-1", "start prompt", "turn-1")

	writeMockNotification(t, enc, "item/completed", map[string]any{
		"item": map[string]any{
			"type":      "mcpToolCall",
			"server":    "discord",
			"tool":      "read_message_history",
			"status":    "completed",
			"arguments": map[string]any{"channel_id": "123"},
			"result":    map[string]any{"messages": []any{}},
		},
	})
	writeMockNotification(t, enc, "item/completed", map[string]any{
		"item": map[string]any{"type": "agentMessage", "text": "start reply"},
	})
	writeMockNotification(t, enc, "turn/completed", map[string]any{
		"turn": map[string]any{"id": "turn-1", "status": "completed"},
	})

	steerReq := readMockRequest(t, dec, turnRequestID+1, "turn/steer")
	steerParams := decodeNotificationParams(steerReq.Params)
	assertTurnSteerParams(t, steerParams, "thread-1", "turn-1", "steer prompt")
	writeMockResponse(t, enc, turnRequestID+1, map[string]any{"turn": map[string]any{"id": "turn-2"}})

	writeMockNotification(t, enc, "item/completed", map[string]any{
		"item": map[string]any{"type": "agentMessage", "text": "steer reply"},
	})
	writeMockNotification(t, enc, "turn/completed", map[string]any{
		"turn": map[string]any{"id": "turn-2", "status": "completed"},
	})
}

func expectMockThreadStart(t *testing.T, dec *json.Decoder, enc *json.Encoder) {
	t.Helper()

	threadReq := readMockRequest(t, dec, threadRequestID, "thread/start")
	threadParams := decodeNotificationParams(threadReq.Params)
	assertThreadConfig(t, threadParams)
	writeMockResponse(t, enc, threadRequestID, map[string]any{"thread": map[string]any{"id": "thread-1"}})
}

func expectMockTurnStart(t *testing.T, dec *json.Decoder, enc *json.Encoder, requestID int, expectedThreadID string, expectedPrompt string, responseTurnID string) {
	t.Helper()

	turnReq := readMockRequest(t, dec, requestID, "turn/start")
	turnParams := decodeNotificationParams(turnReq.Params)
	assertTurnStartParams(t, turnParams, expectedThreadID, expectedPrompt)
	writeMockResponse(t, enc, requestID, map[string]any{"turn": map[string]any{"id": responseTurnID}})
}

func assertThreadConfig(t *testing.T, params map[string]any) {
	t.Helper()

	if params["approvalPolicy"] != "never" {
		t.Fatalf("thread/start approvalPolicy = %#v, want never", params["approvalPolicy"])
	}
	if params["sandbox"] != "workspace-write" {
		t.Fatalf("thread/start sandbox = %#v, want workspace-write", params["sandbox"])
	}
	configRaw, ok := params["config"].(map[string]any)
	if !ok {
		t.Fatalf("thread/start config missing: %#v", params)
	}
	if configRaw["model_reasoning_effort"] != "medium" {
		t.Fatalf("thread/start config.model_reasoning_effort = %#v, want medium", configRaw["model_reasoning_effort"])
	}
	mcpServers, ok := configRaw["mcp_servers"].(map[string]any)
	if !ok {
		t.Fatalf("thread/start config.mcp_servers missing: %#v", configRaw)
	}
	discord, ok := mcpServers["discord"].(map[string]any)
	if !ok {
		t.Fatalf("thread/start config.mcp_servers.discord missing: %#v", mcpServers)
	}
	if discord["url"] != "http://127.0.0.1:39393/mcp" {
		t.Fatalf("thread/start config.mcp_servers.discord.url = %#v", discord["url"])
	}
}

func assertTurnStartParams(t *testing.T, params map[string]any, expectedThreadID string, expectedPrompt string) {
	t.Helper()

	if params["threadId"] != expectedThreadID {
		t.Fatalf("turn/start threadId = %#v, want %q", params["threadId"], expectedThreadID)
	}
	if _, ok := params["expectedTurnId"]; ok {
		t.Fatalf("turn/start expectedTurnId should be absent: %#v", params["expectedTurnId"])
	}
	assertTurnInputParams(t, params, expectedPrompt)
}

func assertTurnSteerParams(t *testing.T, params map[string]any, expectedThreadID string, expectedTurnID string, expectedPrompt string) {
	t.Helper()

	if params["threadId"] != expectedThreadID {
		t.Fatalf("turn/steer threadId = %#v, want %q", params["threadId"], expectedThreadID)
	}
	if params["expectedTurnId"] != expectedTurnID {
		t.Fatalf("turn/steer expectedTurnId = %#v, want %q", params["expectedTurnId"], expectedTurnID)
	}
	assertTurnInputParams(t, params, expectedPrompt)
}

func assertTurnInputParams(t *testing.T, params map[string]any, expectedPrompt string) {
	t.Helper()

	inputRaw, ok := params["input"].([]any)
	if !ok || len(inputRaw) != 1 {
		t.Fatalf("turn input = %#v, want single text input", params["input"])
	}
	inputItem, ok := inputRaw[0].(map[string]any)
	if !ok {
		t.Fatalf("turn input[0] = %#v, want object", inputRaw[0])
	}
	if inputItem["type"] != "text" {
		t.Fatalf("turn input[0].type = %#v, want text", inputItem["type"])
	}
	if inputItem["text"] != expectedPrompt {
		t.Fatalf("turn input[0].text = %#v, want %q", inputItem["text"], expectedPrompt)
	}
	textElements, ok := inputItem["text_elements"].([]any)
	if !ok {
		t.Fatalf("turn input[0].text_elements = %#v, want []", inputItem["text_elements"])
	}
	if len(textElements) != 0 {
		t.Fatalf("turn input[0].text_elements len = %d, want 0", len(textElements))
	}
}

func readRawMessage(t *testing.T, dec *json.Decoder) rpcMessage {
	t.Helper()
	var msg rpcMessage
	if err := dec.Decode(&msg); err != nil {
		t.Fatalf("decode json-rpc message: %v", err)
	}
	return msg
}

func readMockRequest(t *testing.T, dec *json.Decoder, id int, method string) rpcMessage {
	t.Helper()
	msg := readRawMessage(t, dec)
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
	msg := readRawMessage(t, dec)
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

func writeMockRequestFromServer(t *testing.T, enc *json.Encoder, id json.RawMessage, method string, params any) {
	t.Helper()
	payload := map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
	}
	var typedID any
	if err := json.Unmarshal(id, &typedID); err != nil {
		t.Fatalf("decode id: %v", err)
	}
	payload["id"] = typedID

	if err := enc.Encode(payload); err != nil {
		t.Fatalf("encode server request %s: %v", method, err)
	}
}
