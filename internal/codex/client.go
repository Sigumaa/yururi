package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"github.com/sigumaa/yururi/internal/config"
)

const (
	initRequestID   = 1
	threadRequestID = 2
	turnRequestID   = 3
)

type Client struct {
	command         string
	args            []string
	model           string
	reasoningEffort string
	workspaceDir    string
	homeDir         string
	mcpURL          string

	mu      sync.Mutex
	session *appServerSession
}

type appServerSession struct {
	cmd           *exec.Cmd
	stdin         io.WriteCloser
	enc           *json.Encoder
	dec           *json.Decoder
	nextRequestID int
}

type TurnInput struct {
	BaseInstructions      string
	DeveloperInstructions string
	UserPrompt            string
}

type TurnResult struct {
	ThreadID      string
	TurnID        string
	Status        string
	AssistantText string
	ErrorMessage  string
	ToolCalls     []MCPToolCall
}

type MCPToolCall struct {
	Server    string
	Tool      string
	Status    string
	Arguments any
	Result    any
}

type rpcMessage struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func NewClient(cfg config.CodexConfig, mcpURL string) *Client {
	args := append([]string(nil), cfg.Args...)
	return &Client{
		command:         cfg.Command,
		args:            args,
		model:           cfg.Model,
		reasoningEffort: cfg.ReasoningEffort,
		workspaceDir:    cfg.WorkspaceDir,
		homeDir:         cfg.HomeDir,
		mcpURL:          strings.TrimSpace(mcpURL),
	}
}

func (c *Client) RunTurn(ctx context.Context, input TurnInput) (TurnResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	var result TurnResult
	err := c.runWithSessionRetryLocked(ctx, func() error {
		threadID, err := c.startThreadLocked(input)
		if err != nil {
			return err
		}
		result, err = c.startTurnLocked(threadID, input.UserPrompt)
		return err
	})
	if err != nil {
		return TurnResult{}, err
	}
	return result, nil
}

func (c *Client) StartThread(ctx context.Context, input TurnInput) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	var threadID string
	err := c.runWithSessionRetryLocked(ctx, func() error {
		var err error
		threadID, err = c.startThreadLocked(input)
		return err
	})
	if err != nil {
		return "", err
	}
	return threadID, nil
}

func (c *Client) StartTurn(ctx context.Context, threadID string, prompt string) (TurnResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	var result TurnResult
	err := c.runWithSessionRetryLocked(ctx, func() error {
		var err error
		result, err = c.startTurnLocked(threadID, prompt)
		return err
	})
	if err != nil {
		return TurnResult{}, err
	}
	return result, nil
}

func (c *Client) SteerTurn(ctx context.Context, threadID string, expectedTurnID string, prompt string) (TurnResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	var result TurnResult
	err := c.runWithSessionRetryLocked(ctx, func() error {
		var err error
		result, err = c.steerTurnLocked(threadID, expectedTurnID, prompt)
		return err
	})
	if err != nil {
		return TurnResult{}, err
	}
	return result, nil
}

func (c *Client) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stopSessionLocked()
}

func (c *Client) ensureSessionLocked() error {
	if c.session != nil {
		return nil
	}

	cmd := exec.Command(c.command, c.args...)
	if c.workspaceDir != "" {
		cmd.Dir = c.workspaceDir
	}
	cmd.Env = withCodexHomeEnv(os.Environ(), c.homeDir)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("codex stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("codex stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("codex stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start codex: %w", err)
	}
	go io.Copy(io.Discard, stderr)

	dec := json.NewDecoder(stdout)
	dec.UseNumber()
	c.session = &appServerSession{
		cmd:           cmd,
		stdin:         stdin,
		enc:           json.NewEncoder(stdin),
		dec:           dec,
		nextRequestID: initRequestID,
	}

	initID := c.nextRequestIDLocked()
	if err := sendRequest(c.session.enc, initID, "initialize", map[string]any{
		"capabilities": nil,
		"clientInfo": map[string]any{
			"name":    "yururi",
			"version": "phase-full",
		},
	}); err != nil {
		c.stopSessionLocked()
		return err
	}
	initResp, err := readUntilResponse(c.session.dec, c.session.enc, initID, nil)
	if err != nil {
		c.stopSessionLocked()
		return fmt.Errorf("read initialize response: %w", err)
	}
	if initResp.Error != nil {
		c.stopSessionLocked()
		return rpcCallError("initialize", initResp.Error)
	}

	if err := sendNotification(c.session.enc, "initialized", map[string]any{}); err != nil {
		c.stopSessionLocked()
		return err
	}

	return nil
}

func (c *Client) runWithSessionRetryLocked(ctx context.Context, run func() error) error {
	if ctx == nil {
		ctx = context.Background()
	}

	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := c.ensureSessionLocked(); err != nil {
			return err
		}
		if err := run(); err == nil {
			return nil
		} else {
			lastErr = err
			c.stopSessionLocked()
		}
	}
	return lastErr
}

func (c *Client) startThreadLocked(input TurnInput) (string, error) {
	if c.session == nil {
		return "", errors.New("codex session is not initialized")
	}

	threadReqID := c.nextRequestIDLocked()
	if err := sendRequest(c.session.enc, threadReqID, "thread/start", threadStartParams(input, c.model, c.workspaceDir, c.reasoningEffort, c.mcpURL)); err != nil {
		return "", err
	}
	threadResp, err := readUntilResponse(c.session.dec, c.session.enc, threadReqID, nil)
	if err != nil {
		return "", fmt.Errorf("read thread/start response: %w", err)
	}
	if threadResp.Error != nil {
		return "", rpcCallError("thread/start", threadResp.Error)
	}
	threadID, err := extractThreadID(threadResp.Result)
	if err != nil {
		return "", fmt.Errorf("extract thread id: %w", err)
	}
	return threadID, nil
}

func (c *Client) startTurnLocked(threadID string, prompt string) (TurnResult, error) {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return TurnResult{}, errors.New("thread id is required")
	}

	result, err := c.runTurnRequestLocked("turn/start", turnStartParams(threadID, prompt))
	if err != nil {
		return TurnResult{}, err
	}
	result.ThreadID = threadID
	return result, nil
}

func (c *Client) steerTurnLocked(threadID string, expectedTurnID string, prompt string) (TurnResult, error) {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return TurnResult{}, errors.New("thread id is required")
	}
	expectedTurnID = strings.TrimSpace(expectedTurnID)
	if expectedTurnID == "" {
		return TurnResult{}, errors.New("expected turn id is required")
	}

	result, err := c.runTurnRequestLocked("turn/steer", turnSteerParams(threadID, expectedTurnID, prompt))
	if err != nil {
		return TurnResult{}, err
	}
	result.ThreadID = threadID
	return result, nil
}

func (c *Client) runTurnRequestLocked(method string, params map[string]any) (TurnResult, error) {
	if c.session == nil {
		return TurnResult{}, errors.New("codex session is not initialized")
	}

	turnReqID := c.nextRequestIDLocked()
	if err := sendRequest(c.session.enc, turnReqID, method, params); err != nil {
		return TurnResult{}, err
	}

	aggregator := newTurnAggregator()
	onNotification := func(msg rpcMessage) {
		method := normalizeMethod(msg.Method)
		params := decodeNotificationParams(msg.Params)
		aggregator.consume(method, params)
	}

	turnResp, err := readUntilResponse(c.session.dec, c.session.enc, turnReqID, onNotification)
	if err != nil {
		return TurnResult{}, fmt.Errorf("read %s response: %w", method, err)
	}
	if turnResp.Error != nil {
		return TurnResult{}, rpcCallError(method, turnResp.Error)
	}
	turnID, _ := extractTurnID(turnResp.Result)
	aggregator.turnID = turnID

	for !aggregator.Completed() {
		msg, err := readOneMessage(c.session.dec)
		if err != nil {
			return TurnResult{}, fmt.Errorf("wait turn/completed: %w", err)
		}
		if msg.Method != "" && len(msg.ID) > 0 {
			if err := handleServerRequest(c.session.enc, msg); err != nil {
				return TurnResult{}, fmt.Errorf("handle server request: %w", err)
			}
			continue
		}
		if msg.Method != "" {
			onNotification(msg)
		}
	}

	return TurnResult{
		TurnID:        aggregator.turnID,
		Status:        aggregator.status,
		AssistantText: aggregator.FinalText(),
		ErrorMessage:  aggregator.errorMessage,
		ToolCalls:     aggregator.toolCalls,
	}, nil
}

func (c *Client) nextRequestIDLocked() int {
	id := c.session.nextRequestID
	c.session.nextRequestID++
	return id
}

func (c *Client) stopSessionLocked() {
	if c.session == nil {
		return
	}
	_ = c.session.stdin.Close()
	if c.session.cmd != nil && c.session.cmd.Process != nil {
		_ = c.session.cmd.Process.Kill()
	}
	if c.session.cmd != nil {
		_ = c.session.cmd.Wait()
	}
	c.session = nil
}

func sendRequest(enc *json.Encoder, id int, method string, params any) error {
	payload := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
	}
	if params != nil {
		payload["params"] = params
	}
	if err := enc.Encode(payload); err != nil {
		return fmt.Errorf("send request %s: %w", method, err)
	}
	return nil
}

func sendResponse(enc *json.Encoder, id json.RawMessage, result any, rpcErr *rpcError) error {
	payload := map[string]any{
		"jsonrpc": "2.0",
	}
	if len(id) > 0 {
		var typed any
		if err := json.Unmarshal(id, &typed); err == nil {
			payload["id"] = typed
		} else {
			payload["id"] = string(id)
		}
	}
	if rpcErr != nil {
		payload["error"] = rpcErr
	} else {
		payload["result"] = result
	}
	if err := enc.Encode(payload); err != nil {
		return fmt.Errorf("send response: %w", err)
	}
	return nil
}

func sendNotification(enc *json.Encoder, method string, params any) error {
	payload := map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
	}
	if params != nil {
		payload["params"] = params
	}
	if err := enc.Encode(payload); err != nil {
		return fmt.Errorf("send notification %s: %w", method, err)
	}
	return nil
}

func readUntilResponse(dec *json.Decoder, enc *json.Encoder, id int, onNotification func(rpcMessage)) (rpcMessage, error) {
	for {
		msg, err := readOneMessage(dec)
		if err != nil {
			return rpcMessage{}, err
		}

		if msg.Method != "" && len(msg.ID) > 0 {
			if err := handleServerRequest(enc, msg); err != nil {
				return rpcMessage{}, err
			}
			continue
		}

		if msg.Method != "" && len(msg.ID) == 0 {
			if onNotification != nil {
				onNotification(msg)
			}
			continue
		}

		if len(msg.ID) == 0 {
			continue
		}
		msgID, err := parseID(msg.ID)
		if err != nil {
			continue
		}
		if msgID == id {
			return msg, nil
		}
	}
}

func handleServerRequest(enc *json.Encoder, msg rpcMessage) error {
	method := normalizeMethod(msg.Method)
	switch method {
	case "item_command_execution_request_approval", "item_file_change_request_approval", "exec_command_approval", "apply_patch_approval":
		return sendResponse(enc, msg.ID, map[string]any{"decision": "decline"}, nil)
	case "item_tool_request_user_input":
		answers := map[string]any{}
		params := decodeNotificationParams(msg.Params)
		questionsRaw, ok := getValueAtPath(params, "questions")
		if ok {
			if questions, castOK := questionsRaw.([]any); castOK {
				for _, q := range questions {
					obj, ok := q.(map[string]any)
					if !ok {
						continue
					}
					qid, _ := obj["id"].(string)
					if strings.TrimSpace(qid) == "" {
						continue
					}
					label := pickOptionLabel(obj)
					answers[qid] = map[string]any{"answers": []string{label}}
				}
			}
		}
		return sendResponse(enc, msg.ID, map[string]any{"answers": answers}, nil)
	default:
		return sendResponse(enc, msg.ID, nil, &rpcError{Code: -32601, Message: "unsupported server request: " + msg.Method})
	}
}

func pickOptionLabel(question map[string]any) string {
	optionsRaw, ok := question["options"]
	if !ok {
		return ""
	}
	options, ok := optionsRaw.([]any)
	if !ok || len(options) == 0 {
		return ""
	}
	best := ""
	for _, raw := range options {
		option, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		label, _ := option["label"].(string)
		lowered := strings.ToLower(strings.TrimSpace(label))
		if lowered == "" {
			continue
		}
		if best == "" {
			best = label
		}
		if strings.Contains(lowered, "decline") || strings.Contains(lowered, "cancel") {
			return label
		}
	}
	return best
}

func readOneMessage(dec *json.Decoder) (rpcMessage, error) {
	var msg rpcMessage
	if err := dec.Decode(&msg); err != nil {
		return rpcMessage{}, err
	}
	return msg, nil
}

func parseID(raw json.RawMessage) (int, error) {
	if len(raw) == 0 {
		return 0, errors.New("empty id")
	}
	var n json.Number
	if err := json.Unmarshal(raw, &n); err == nil {
		return strconv.Atoi(n.String())
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return strconv.Atoi(s)
	}
	return 0, errors.New("unsupported id type")
}

func decodeNotificationParams(raw json.RawMessage) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var params map[string]any
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil
	}
	return params
}

func getValueAtPath(params map[string]any, path ...string) (any, bool) {
	if len(path) == 0 || params == nil {
		return nil, false
	}

	var current any = params
	for _, key := range path {
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		next, ok := object[key]
		if !ok {
			return nil, false
		}
		current = next
	}
	return current, true
}

func getStringAtPath(params map[string]any, path ...string) (string, bool) {
	value, ok := getValueAtPath(params, path...)
	if !ok {
		return "", false
	}
	text, ok := value.(string)
	if !ok {
		return "", false
	}
	return text, true
}

func getFirstNonEmptyStringAtPaths(params map[string]any, paths ...[]string) string {
	for _, path := range paths {
		text, ok := getStringAtPath(params, path...)
		if !ok {
			continue
		}
		text = strings.TrimSpace(text)
		if text != "" {
			return text
		}
	}
	return ""
}

type turnAggregator struct {
	deltaBuilder      strings.Builder
	agentMessageText  string
	completed         bool
	status            string
	errorMessage      string
	turnID            string
	toolCalls         []MCPToolCall
	turnCompletedText string
}

func newTurnAggregator() *turnAggregator {
	return &turnAggregator{}
}

func (a *turnAggregator) consume(method string, params map[string]any) {
	switch {
	case isAgentMessageDeltaMethod(method):
		a.consumeAgentMessageDelta(params)
	case isItemCompletedMethod(method):
		a.consumeItemCompleted(params)
	case isTurnCompletedMethod(method):
		a.consumeTurnCompleted(params)
	case method == "error":
		a.consumeError(params)
	}
}

func (a *turnAggregator) consumeAgentMessageDelta(params map[string]any) {
	delta, ok := getStringAtPath(params, "delta")
	if !ok {
		return
	}
	a.deltaBuilder.WriteString(delta)
}

func (a *turnAggregator) consumeItemCompleted(params map[string]any) {
	itemRaw, ok := getValueAtPath(params, "item")
	if !ok {
		return
	}
	item, ok := itemRaw.(map[string]any)
	if !ok {
		return
	}
	itemTypeRaw, _ := item["type"].(string)
	itemType := normalizeItemType(itemTypeRaw)

	switch itemType {
	case "agentmessage":
		if text := extractAgentMessageText(item); strings.TrimSpace(text) != "" {
			a.agentMessageText = strings.TrimSpace(text)
		}
	case "mcptoolcall":
		call := MCPToolCall{}
		call.Server, _ = item["server"].(string)
		call.Tool, _ = item["tool"].(string)
		call.Arguments = item["arguments"]
		call.Result = item["result"]
		call.Status = normalizeToolStatus(item["status"])
		a.toolCalls = append(a.toolCalls, call)
	}
}

func (a *turnAggregator) consumeTurnCompleted(params map[string]any) {
	a.completed = true
	if id, ok := getStringAtPath(params, "turn", "id"); ok && strings.TrimSpace(id) != "" {
		a.turnID = id
	}
	if status := getFirstNonEmptyStringAtPaths(params, []string{"turn", "status"}, []string{"status"}); status != "" {
		a.status = status
	}
	if errMsg := getFirstNonEmptyStringAtPaths(params,
		[]string{"turn", "error_message"},
		[]string{"turn", "error", "message"},
		[]string{"error", "message"},
	); errMsg != "" {
		a.errorMessage = errMsg
	}
	if text := getFirstNonEmptyStringAtPaths(params,
		[]string{"output", "text"},
		[]string{"output", "content"},
		[]string{"result", "text"},
		[]string{"result", "content"},
		[]string{"output"},
		[]string{"result"},
		[]string{"text"},
		[]string{"content"},
	); text != "" {
		a.turnCompletedText = text
	}
}

func (a *turnAggregator) consumeError(params map[string]any) {
	if errMsg := getFirstNonEmptyStringAtPaths(params,
		[]string{"error", "message"},
		[]string{"message"},
	); errMsg != "" {
		a.errorMessage = errMsg
	}
}

func (a *turnAggregator) Completed() bool {
	return a.completed
}

func (a *turnAggregator) FinalText() string {
	if text := strings.TrimSpace(a.agentMessageText); text != "" {
		return text
	}
	if text := strings.TrimSpace(a.deltaBuilder.String()); text != "" {
		return text
	}
	return strings.TrimSpace(a.turnCompletedText)
}

func normalizeMethod(method string) string {
	method = strings.TrimSpace(method)
	if method == "" {
		return ""
	}

	var out []rune
	for _, r := range method {
		switch {
		case unicode.IsUpper(r):
			if len(out) > 0 && out[len(out)-1] != '_' && (unicode.IsLower(out[len(out)-1]) || unicode.IsDigit(out[len(out)-1])) {
				out = append(out, '_')
			}
			out = append(out, unicode.ToLower(r))
		case unicode.IsLower(r) || unicode.IsDigit(r):
			out = append(out, r)
		default:
			if len(out) == 0 || out[len(out)-1] == '_' {
				continue
			}
			out = append(out, '_')
		}
	}
	return strings.Trim(string(out), "_")
}

func normalizeItemType(raw string) string {
	raw = strings.TrimSpace(strings.ToLower(raw))
	raw = strings.ReplaceAll(raw, "_", "")
	return raw
}

func normalizeToolStatus(raw any) string {
	status, _ := raw.(string)
	status = strings.TrimSpace(strings.ToLower(status))
	switch status {
	case "in_progress", "inprogress":
		return "inProgress"
	case "failed":
		return "failed"
	case "completed":
		return "completed"
	default:
		if status == "" {
			return "unknown"
		}
		return status
	}
}

func extractAgentMessageText(item map[string]any) string {
	if text, ok := item["text"].(string); ok {
		return text
	}
	messageRaw, ok := item["message"]
	if !ok {
		return ""
	}
	message, ok := messageRaw.(map[string]any)
	if !ok {
		return ""
	}
	contentRaw, ok := message["content"]
	if !ok {
		return ""
	}
	entries, ok := contentRaw.([]any)
	if !ok {
		return ""
	}
	parts := make([]string, 0, len(entries))
	for _, entryRaw := range entries {
		entry, ok := entryRaw.(map[string]any)
		if !ok {
			continue
		}
		typeName, _ := entry["type"].(string)
		if strings.TrimSpace(strings.ToLower(typeName)) != "text" {
			continue
		}
		text, _ := entry["text"].(string)
		if strings.TrimSpace(text) == "" {
			continue
		}
		parts = append(parts, text)
	}
	return strings.Join(parts, "")
}

func isAgentMessageDeltaMethod(method string) bool {
	return strings.Contains(method, "agent_message_delta")
}

func isItemCompletedMethod(method string) bool {
	return method == "item_completed"
}

func isTurnCompletedMethod(method string) bool {
	return method == "turn_completed"
}

func threadStartParams(input TurnInput, model string, cwd string, reasoningEffort string, mcpURL string) map[string]any {
	params := map[string]any{
		"approvalPolicy":         "never",
		"sandbox":                "workspace-write",
		"baseInstructions":       input.BaseInstructions,
		"developerInstructions":  input.DeveloperInstructions,
		"ephemeral":              true,
		"personality":            "friendly",
		"experimentalRawEvents":  false,
		"persistExtendedHistory": false,
	}
	if strings.TrimSpace(model) != "" {
		params["model"] = strings.TrimSpace(model)
	}
	if strings.TrimSpace(cwd) != "" {
		params["cwd"] = strings.TrimSpace(cwd)
	}

	cfg := map[string]any{}
	if strings.TrimSpace(reasoningEffort) != "" {
		cfg["model_reasoning_effort"] = strings.TrimSpace(reasoningEffort)
	}
	if strings.TrimSpace(mcpURL) != "" {
		cfg["mcp_servers"] = map[string]any{
			"discord": map[string]any{
				"url": strings.TrimSpace(mcpURL),
			},
		}
	}
	if len(cfg) > 0 {
		params["config"] = cfg
	}

	return params
}

func turnStartParams(threadID string, prompt string) map[string]any {
	return map[string]any{
		"threadId": threadID,
		"input":    turnInput(prompt),
	}
}

func turnSteerParams(threadID string, expectedTurnID string, prompt string) map[string]any {
	return map[string]any{
		"threadId":       threadID,
		"expectedTurnId": expectedTurnID,
		"input":          turnInput(prompt),
	}
}

func turnInput(prompt string) []map[string]any {
	return []map[string]any{{
		"type":          "text",
		"text":          prompt,
		"text_elements": []any{},
	}}
}

func extractThreadID(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", errors.New("thread/start result is empty")
	}
	var direct string
	if err := json.Unmarshal(raw, &direct); err == nil {
		direct = strings.TrimSpace(direct)
		if direct != "" {
			return direct, nil
		}
	}

	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		return "", fmt.Errorf("decode thread/start result: %w", err)
	}

	if id := extractThreadIDFromThread(result); id != "" {
		return id, nil
	}
	if id := extractTopLevelString(result, "threadId", "thread_id", "id"); id != "" {
		return id, nil
	}
	return "", errors.New("thread id not found in thread/start result")
}

func extractThreadIDFromThread(result map[string]any) string {
	threadRaw, ok := result["thread"]
	if !ok {
		return ""
	}

	thread, ok := threadRaw.(map[string]any)
	if !ok {
		return ""
	}

	idRaw, ok := thread["id"]
	if !ok {
		return ""
	}

	id, ok := idRaw.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(id)
}

func extractTopLevelString(result map[string]any, keys ...string) string {
	for _, key := range keys {
		raw, ok := result[key]
		if !ok {
			continue
		}
		value, ok := raw.(string)
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func extractTurnID(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	var direct string
	if err := json.Unmarshal(raw, &direct); err == nil {
		direct = strings.TrimSpace(direct)
		if direct != "" {
			return direct, true
		}
	}
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		return "", false
	}
	if turnRaw, ok := result["turn"]; ok {
		if turn, ok := turnRaw.(map[string]any); ok {
			if id, ok := turn["id"].(string); ok && strings.TrimSpace(id) != "" {
				return id, true
			}
		}
	}
	if id := extractTopLevelString(result, "turnId", "turn_id", "id"); id != "" {
		return id, true
	}
	return "", false
}

func withCodexHomeEnv(base []string, homeDir string) []string {
	env := append([]string(nil), base...)
	if homeDir == "" {
		return env
	}
	return upsertEnv(env, "CODEX_HOME", homeDir)
}

func upsertEnv(env []string, key string, value string) []string {
	prefix := key + "="
	entry := prefix + value
	replaced := false
	for i := range env {
		if strings.HasPrefix(env[i], prefix) {
			env[i] = entry
			replaced = true
		}
	}
	if !replaced {
		env = append(env, entry)
	}
	return env
}

func rpcCallError(method string, err *rpcError) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s failed: code=%d message=%s", method, err.Code, err.Message)
}
