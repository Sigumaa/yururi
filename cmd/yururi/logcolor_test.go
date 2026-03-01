package main

import "testing"

func TestEventFromLogLine(t *testing.T) {
	t.Parallel()

	line := `2026/02/27 01:23:45 event=heartbeat_turn_failed run_id=hb-1 err="timeout"`
	got := eventFromLogLine(line)
	if got != "heartbeat_turn_failed" {
		t.Fatalf("eventFromLogLine() = %q, want %q", got, "heartbeat_turn_failed")
	}
}

func TestColorForLine(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		line string
		want string
	}{
		{
			name: "failed is red",
			line: "event=heartbeat_turn_failed run_id=h1",
			want: ansiRed,
		},
		{
			name: "completed is green",
			line: "event=mcp_tool_completed tool=send_message",
			want: ansiGreen,
		},
		{
			name: "suppressed is yellow",
			line: "event=message_times_suppressed run_id=msg-1",
			want: ansiYellow,
		},
		{
			name: "tool call is magenta",
			line: "event=message_tool_call run_id=msg-1",
			want: ansiMagenta,
		},
		{
			name: "started is blue",
			line: "event=mcp_tool_started tool=send_message",
			want: ansiBlue,
		},
		{
			name: "tick is cyan",
			line: "event=heartbeat_tick run_id=hb-1",
			want: ansiCyan,
		},
		{
			name: "no event no color",
			line: "plain text log",
			want: "",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := colorForLine(tc.line); got != tc.want {
				t.Fatalf("colorForLine(%q) = %q, want %q", tc.line, got, tc.want)
			}
		})
	}
}

func TestColorizeLogLine(t *testing.T) {
	t.Parallel()

	line := "event=heartbeat_turn_failed run_id=hb-1"
	got := colorizeLogLine(line)
	if got != ansiRed+line+ansiReset {
		t.Fatalf("colorizeLogLine() = %q, want colorized line", got)
	}
}

func TestBoolFromEnvValue(t *testing.T) {
	t.Setenv("YURURI_LOG_COLOR", "true")
	if got, ok := boolFromEnv("YURURI_LOG_COLOR"); !ok || !got {
		t.Fatalf("boolFromEnv(true) = (%v, %v), want (true, true)", got, ok)
	}
	t.Setenv("YURURI_LOG_COLOR", "false")
	if got, ok := boolFromEnv("YURURI_LOG_COLOR"); !ok || got {
		t.Fatalf("boolFromEnv(false) = (%v, %v), want (false, true)", got, ok)
	}
	t.Setenv("YURURI_LOG_COLOR", "invalid")
	if got, ok := boolFromEnv("YURURI_LOG_COLOR"); ok || got {
		t.Fatalf("boolFromEnv(invalid) = (%v, %v), want (false, false)", got, ok)
	}
}
