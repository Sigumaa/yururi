package main

import (
	"io"
	"log"
	"os"
	"strings"
)

const (
	ansiReset   = "\x1b[0m"
	ansiRed     = "\x1b[31m"
	ansiGreen   = "\x1b[32m"
	ansiYellow  = "\x1b[33m"
	ansiBlue    = "\x1b[34m"
	ansiMagenta = "\x1b[35m"
	ansiCyan    = "\x1b[36m"
)

func configureLogOutput(dst io.Writer) {
	if dst == nil {
		return
	}
	log.SetOutput(&colorLogWriter{
		dst:     dst,
		enabled: shouldEnableLogColor(),
	})
}

type colorLogWriter struct {
	dst     io.Writer
	enabled bool
}

func (w *colorLogWriter) Write(p []byte) (int, error) {
	if !w.enabled {
		return w.dst.Write(p)
	}
	colored := colorizeLogLine(string(p))
	if _, err := io.WriteString(w.dst, colored); err != nil {
		return 0, err
	}
	return len(p), nil
}

func shouldEnableLogColor() bool {
	if enabled, ok := boolFromEnv("YURURI_LOG_COLOR"); ok {
		return enabled
	}
	if _, disabled := os.LookupEnv("NO_COLOR"); disabled {
		return false
	}
	info, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return (info.Mode() & os.ModeCharDevice) != 0
}

func boolFromEnv(key string) (bool, bool) {
	value, ok := os.LookupEnv(key)
	if !ok {
		return false, false
	}
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true, true
	case "0", "false", "no", "off":
		return false, true
	default:
		return false, false
	}
}

func colorizeLogLine(line string) string {
	color := colorForLine(line)
	if color == "" {
		return line
	}
	return color + line + ansiReset
}

func colorForLine(line string) string {
	event := eventFromLogLine(line)
	if event == "" {
		return ""
	}
	switch {
	case strings.Contains(event, "failed") || strings.Contains(event, "error"):
		return ansiRed
	case strings.Contains(event, "suppressed"):
		return ansiYellow
	case strings.Contains(event, "tool_call"):
		return ansiMagenta
	case strings.Contains(event, "completed") || strings.Contains(event, "posted") || strings.Contains(event, "resolved"):
		return ansiGreen
	case strings.Contains(event, "started"):
		return ansiBlue
	case strings.Contains(event, "tick"):
		return ansiCyan
	default:
		return ""
	}
}

func eventFromLogLine(line string) string {
	i := strings.Index(line, "event=")
	if i < 0 {
		return ""
	}
	raw := line[i+len("event="):]
	if end := strings.IndexAny(raw, " \t\r\n"); end >= 0 {
		raw = raw[:end]
	}
	return strings.TrimSpace(raw)
}
