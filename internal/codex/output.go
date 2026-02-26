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

	start := strings.Index(payload, "{")
	end := strings.LastIndex(payload, "}")
	if start < 0 || end <= start {
		return Decision{}, fmt.Errorf("output is not json: %q", raw)
	}
	payload = payload[start : end+1]

	var d Decision
	if err := json.Unmarshal([]byte(payload), &d); err != nil {
		return Decision{}, fmt.Errorf("parse decision json: %w", err)
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
