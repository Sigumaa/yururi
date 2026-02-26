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
	cmd.Env = os.Environ()
	if c.homeDir != "" {
		cmd.Env = append(cmd.Env, "HOME="+c.homeDir)
	}

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

	if err := sendRequest(enc, threadRequestID, "thread/start", map[string]any{}); err != nil {
		return Decision{}, err
	}
	threadResp, err := readUntilResponse(dec, threadRequestID, nil)
	if err != nil {
		return Decision{}, fmt.Errorf("read thread/start response: %w", err)
	}
	if threadResp.Error != nil {
		return Decision{}, rpcCallError("thread/start", threadResp.Error)
	}
	threadID := extractString(threadResp.Result, "thread_id", "threadId", "id")
	if threadID == "" {
		threadID = "default"
	}

	prompt := buildPrompt(input)
	turnParams := map[string]any{
		"threadId": threadID,
		"input": []map[string]any{{
			"type": "text",
			"text": prompt,
		}},
	}
	if err := sendRequest(enc, turnRequestID, "turn/start", turnParams); err != nil {
		return Decision{}, err
	}

	var builder strings.Builder
	completed := false
	onNotification := func(msg rpcMessage) {
		method := normalizeMethod(msg.Method)
		switch {
		case strings.Contains(method, "agent_message_delta"):
			delta := strings.TrimSpace(extractString(msg.Params, "delta", "text", "content"))
			if delta != "" {
				builder.WriteString(delta)
			}
		case method == "turn/completed" || method == "turn_completed":
			if builder.Len() == 0 {
				finalText := strings.TrimSpace(extractString(msg.Params, "output", "result", "text", "content", "final"))
				if finalText != "" {
					builder.WriteString(finalText)
				}
			}
			completed = true
		}
	}

	turnResp, err := readUntilResponse(dec, turnRequestID, onNotification)
	if err != nil {
		return Decision{}, fmt.Errorf("read turn/start response: %w", err)
	}
	if turnResp.Error != nil {
		return Decision{}, rpcCallError("turn/start", turnResp.Error)
	}

	for !completed {
		msg, err := readOneMessage(dec)
		if err != nil {
			return Decision{}, fmt.Errorf("wait turn/completed: %w", err)
		}
		if msg.Method == "" {
			continue
		}
		onNotification(msg)
	}

	decision, err := ParseDecisionOutput(builder.String())
	if err != nil {
		return Decision{}, fmt.Errorf("parse model output: %w", err)
	}
	return decision, nil
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

func extractString(raw json.RawMessage, keys ...string) string {
	if len(raw) == 0 {
		return ""
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return ""
	}
	return extractStringFromAny(v, keys...)
}

func extractStringFromAny(v any, keys ...string) string {
	switch value := v.(type) {
	case string:
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			return trimmed
		}
	case map[string]any:
		for _, key := range keys {
			if raw, ok := value[key]; ok {
				if s := extractStringFromAny(raw, keys...); s != "" {
					return s
				}
			}
		}
		for _, raw := range value {
			if s := extractStringFromAny(raw, keys...); s != "" {
				return s
			}
		}
	case []any:
		for _, raw := range value {
			if s := extractStringFromAny(raw, keys...); s != "" {
				return s
			}
		}
	}
	return ""
}

func normalizeMethod(method string) string {
	return strings.TrimSpace(strings.ToLower(method))
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
