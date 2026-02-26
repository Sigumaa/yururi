package codex

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type Decision struct {
	Action  string `json:"action"`
	Content string `json:"content,omitempty"`
}

func ParseDecisionOutput(raw string) (Decision, error) {
	payload := strings.TrimSpace(raw)
	if payload == "" {
		return Decision{}, errors.New("empty output")
	}

	var lastErr error
	for i := 0; i < len(payload); i++ {
		if payload[i] != '{' {
			continue
		}

		end := findJSONObjectEnd(payload, i)
		if end < 0 {
			continue
		}

		d, err := parseDecisionJSON(payload[i : end+1])
		if err == nil {
			return d, nil
		}
		lastErr = err
	}

	if lastErr != nil {
		return Decision{}, fmt.Errorf("parse decision json: %w", lastErr)
	}
	return Decision{}, fmt.Errorf("output is not json: %q", raw)
}

func parseDecisionJSON(payload string) (Decision, error) {
	var d Decision
	if err := json.Unmarshal([]byte(payload), &d); err != nil {
		return Decision{}, err
	}

	d.Action = strings.TrimSpace(d.Action)
	d.Content = strings.TrimSpace(d.Content)

	switch d.Action {
	case "noop":
		d.Content = ""
		return d, nil
	case "reply":
		if d.Content == "" {
			return Decision{}, errors.New("reply action requires content")
		}
		return d, nil
	default:
		return Decision{}, fmt.Errorf("unsupported action: %s", d.Action)
	}
}

func findJSONObjectEnd(payload string, start int) int {
	if start < 0 || start >= len(payload) || payload[start] != '{' {
		return -1
	}

	depth := 0
	inString := false
	escaped := false

	for i := start; i < len(payload); i++ {
		ch := payload[i]
		if inString {
			switch {
			case escaped:
				escaped = false
			case ch == '\\':
				escaped = true
			case ch == '"':
				inString = false
			}
			continue
		}

		switch ch {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i
			}
			if depth < 0 {
				return -1
			}
		}
	}
	return -1
}
