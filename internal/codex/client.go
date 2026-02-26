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
	"unicode"

	"github.com/sigumaa/yururi/internal/config"
)

const (
	initRequestID   = 1
	threadRequestID = 2
	turnRequestID   = 3
)

type Client struct {
	command      string
	args         []string
	workspaceDir string
	homeDir      string
}

type TurnInput struct {
	AuthorID string
	Content  string
	IsOwner  bool
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

func NewClient(cfg config.CodexConfig) *Client {
	args := append([]string(nil), cfg.Args...)
	return &Client{
		command:      cfg.Command,
		args:         args,
		workspaceDir: cfg.WorkspaceDir,
		homeDir:      cfg.HomeDir,
	}
}

func (c *Client) RunTurn(ctx context.Context, input TurnInput) (Decision, error) {
	cmd := exec.CommandContext(ctx, c.command, c.args...)
	if c.workspaceDir != "" {
		cmd.Dir = c.workspaceDir
	}
	cmd.Env = withCodexHomeEnv(os.Environ(), c.homeDir)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return Decision{}, fmt.Errorf("codex stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Decision{}, fmt.Errorf("codex stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return Decision{}, fmt.Errorf("codex stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return Decision{}, fmt.Errorf("start codex: %w", err)
	}
	defer func() {
		_ = stdin.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	}()
	go io.Copy(io.Discard, stderr)

	enc := json.NewEncoder(stdin)
	dec := json.NewDecoder(stdout)
	dec.UseNumber()

	if err := sendRequest(enc, initRequestID, "initialize", map[string]any{
		"clientInfo": map[string]any{
			"name":    "yururi",
			"version": "phase1",
		},
	}); err != nil {
		return Decision{}, err
	}
	initResp, err := readUntilResponse(dec, initRequestID, nil)
	if err != nil {
		return Decision{}, fmt.Errorf("read initialize response: %w", err)
	}
	if initResp.Error != nil {
		return Decision{}, rpcCallError("initialize", initResp.Error)
	}

	if err := sendNotification(enc, "initialized", map[string]any{}); err != nil {
		return Decision{}, err
	}

	developerInstructions := buildDeveloperInstructions(input.IsOwner)
	if err := sendRequest(enc, threadRequestID, "thread/start", threadStartParams(developerInstructions)); err != nil {
		return Decision{}, err
	}
	threadResp, err := readUntilResponse(dec, threadRequestID, nil)
	if err != nil {
		return Decision{}, fmt.Errorf("read thread/start response: %w", err)
	}
	if threadResp.Error != nil {
		return Decision{}, rpcCallError("thread/start", threadResp.Error)
	}
	threadID, err := extractThreadID(threadResp.Result)
	if err != nil {
		return Decision{}, fmt.Errorf("extract thread id: %w", err)
	}

	prompt := buildPrompt(input)
	turnParams := turnStartParams(threadID, prompt)
	if err := sendRequest(enc, turnRequestID, "turn/start", turnParams); err != nil {
		return Decision{}, err
	}

	aggregator := newTurnTextAggregator()
	onNotification := func(msg rpcMessage) {
		method := normalizeMethod(msg.Method)
		params := decodeNotificationParams(msg.Params)
		aggregator.consume(method, params)
	}

	turnResp, err := readUntilResponse(dec, turnRequestID, onNotification)
	if err != nil {
		return Decision{}, fmt.Errorf("read turn/start response: %w", err)
	}
	if turnResp.Error != nil {
		return Decision{}, rpcCallError("turn/start", turnResp.Error)
	}

	for !aggregator.Completed() {
		msg, err := readOneMessage(dec)
		if err != nil {
			return Decision{}, fmt.Errorf("wait turn/completed: %w", err)
		}
		if msg.Method == "" {
			continue
		}
		onNotification(msg)
	}

	decision, err := parseDecisionOrNoop(aggregator.FinalText())
	if err != nil {
		return Decision{}, fmt.Errorf("parse model output: %w", err)
	}
	return decision, nil
}

func parseDecisionOrNoop(raw string) (Decision, error) {
	if strings.TrimSpace(raw) == "" {
		return Decision{Action: "noop"}, nil
	}
	return ParseDecisionOutput(raw)
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

func readUntilResponse(dec *json.Decoder, id int, onNotification func(rpcMessage)) (rpcMessage, error) {
	for {
		msg, err := readOneMessage(dec)
		if err != nil {
			return rpcMessage{}, err
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

type turnTextAggregator struct {
	deltaBuilder      strings.Builder
	agentMessageText  string
	turnCompletedText string
	completed         bool
}

func newTurnTextAggregator() *turnTextAggregator {
	return &turnTextAggregator{}
}

func (a *turnTextAggregator) consume(method string, params map[string]any) {
	switch {
	case isAgentMessageDeltaMethod(method):
		a.consumeAgentMessageDelta(params)
	case isItemCompletedMethod(method):
		a.consumeItemCompleted(params)
	case isTurnCompletedMethod(method):
		a.consumeTurnCompleted(params)
	}
}

func (a *turnTextAggregator) consumeAgentMessageDelta(params map[string]any) {
	delta, ok := getStringAtPath(params, "delta")
	if !ok {
		return
	}
	a.deltaBuilder.WriteString(delta)
}

func (a *turnTextAggregator) consumeItemCompleted(params map[string]any) {
	itemType, ok := getStringAtPath(params, "item", "type")
	if !ok || strings.TrimSpace(itemType) != "agentMessage" {
		return
	}

	text, ok := getStringAtPath(params, "item", "text")
	if !ok {
		return
	}
	if strings.TrimSpace(text) == "" {
		return
	}
	a.agentMessageText = text
}

func (a *turnTextAggregator) consumeTurnCompleted(params map[string]any) {
	a.completed = true
	if text := getFirstNonEmptyStringAtPaths(params,
		[]string{"output", "text"},
		[]string{"output", "content"},
		[]string{"output", "final"},
		[]string{"result", "text"},
		[]string{"result", "content"},
		[]string{"result", "final"},
		[]string{"output"},
		[]string{"result"},
		[]string{"text"},
		[]string{"content"},
		[]string{"final"},
	); text != "" {
		a.turnCompletedText = text
	}
}

func (a *turnTextAggregator) Completed() bool {
	return a.completed
}

func (a *turnTextAggregator) FinalText() string {
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

func isAgentMessageDeltaMethod(method string) bool {
	return strings.Contains(method, "agent_message_delta")
}

func isItemCompletedMethod(method string) bool {
	return method == "item_completed"
}

func isTurnCompletedMethod(method string) bool {
	return method == "turn_completed"
}

func threadStartParams(developerInstructions string) map[string]any {
	return map[string]any{
		"approvalPolicy":        "never",
		"sandbox":               "workspace-write",
		"developerInstructions": developerInstructions,
	}
}

func turnStartParams(threadID string, prompt string) map[string]any {
	return map[string]any{
		"threadId": threadID,
		"input": []map[string]any{{
			"type": "text",
			"text": prompt,
		}},
		"outputSchema": decisionOutputSchema(),
	}
}

func decisionOutputSchema() map[string]any {
	return map[string]any{
		"oneOf": []map[string]any{
			{
				"type": "object",
				"properties": map[string]any{
					"action": map[string]any{
						"const": "noop",
					},
				},
				"required":             []string{"action"},
				"additionalProperties": false,
			},
			{
				"type": "object",
				"properties": map[string]any{
					"action": map[string]any{
						"const": "reply",
					},
					"content": map[string]any{
						"type":      "string",
						"minLength": 1,
					},
				},
				"required":             []string{"action", "content"},
				"additionalProperties": false,
			},
		},
	}
}

func extractThreadID(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", errors.New("thread/start result is empty")
	}

	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		return "", fmt.Errorf("decode thread/start result: %w", err)
	}

	if id := extractThreadIDFromThread(result); id != "" {
		return id, nil
	}
	if id := extractTopLevelString(result, "threadId", "thread_id"); id != "" {
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

func buildPrompt(input TurnInput) string {
	tone := "通常トーン"
	if input.IsOwner {
		tone = "owner_user_idなので少し甘め"
	}

	return fmt.Sprintf(`あなたは「ゆるり」です。可愛い女子大生メイドとして自然に振る舞ってください。
- 出力はJSON文字列のみ
- 形式は厳密に次のどちらか:
  {"action":"noop"}
  {"action":"reply","content":"..."}
- JSON以外の文字を絶対に出力しない
- 不要な返信は避けてよい
- トーン: %s

入力:
- user_id: %s
- message:
%s`, tone, input.AuthorID, input.Content)
}

func buildDeveloperInstructions(isOwner bool) string {
	tone := "通常トーン"
	if isOwner {
		tone = "owner_user_idなので少し甘め"
	}

	return fmt.Sprintf(`あなたは「ゆるり」です。可愛い女子大生メイドとして自然に振る舞ってください。
- 出力はJSON文字列のみ
- actionは"noop"または"reply"のみ
- 形式は厳密に次のどちらか:
  {"action":"noop"}
  {"action":"reply","content":"..."}
- JSON以外の文字を絶対に出力しない
- トーン: %s`, tone)
}
