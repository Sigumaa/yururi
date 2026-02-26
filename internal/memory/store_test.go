package memory

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStoreUpsertAndQuery(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store, err := NewStore(root)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}

	if _, err := store.UpsertUserNote(context.Background(), UserNoteInput{
		UserID: "u1",
		Note:   "このユーザーはGoが得意",
		Source: "discord",
	}); err != nil {
		t.Fatalf("UpsertUserNote() error = %v", err)
	}

	if _, err := store.UpsertChannelIntent(context.Background(), ChannelIntentInput{
		ChannelID: "c1",
		Intent:    "技術雑談中心",
		Policy:    "冗長な返信は避ける",
	}); err != nil {
		t.Fatalf("UpsertChannelIntent() error = %v", err)
	}

	results, err := store.Query(context.Background(), QueryInput{Keyword: "Go", Limit: 10})
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	if len(results) == 0 {
		t.Fatal("Query() got no results")
	}

	if _, err := os.Stat(filepath.Join(root, "index.md")); err != nil {
		t.Fatalf("index.md missing: %v", err)
	}
}

func TestStoreTaskLifecycle(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store, err := NewStore(root)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}

	nextRun := time.Now().UTC().Add(-1 * time.Minute)
	task, err := store.UpsertTask(context.Background(), UpsertTaskInput{
		TaskID:       "daily-news",
		Title:        "daily news",
		Instructions: "毎日ニュースを要約して送る",
		ChannelID:    "chan-1",
		Schedule:     "daily",
		NextRunAt:    nextRun,
	})
	if err != nil {
		t.Fatalf("UpsertTask() error = %v", err)
	}
	if task.TaskID != "daily-news" {
		t.Fatalf("task.TaskID = %q, want daily-news", task.TaskID)
	}

	due, err := store.ClaimDueTasks(context.Background(), time.Now().UTC(), 10)
	if err != nil {
		t.Fatalf("ClaimDueTasks() error = %v", err)
	}
	if len(due) != 1 {
		t.Fatalf("ClaimDueTasks() len = %d, want 1", len(due))
	}
	if due[0].LastRunAt.IsZero() {
		t.Fatalf("ClaimDueTasks() LastRunAt is zero")
	}
	if due[0].NextRunAt.IsZero() {
		t.Fatalf("ClaimDueTasks() NextRunAt is zero")
	}
	if !due[0].NextRunAt.After(time.Now().UTC()) {
		t.Fatalf("ClaimDueTasks() NextRunAt = %s, want future", due[0].NextRunAt)
	}
}

func TestNextRunFromSchedule(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 2, 26, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name     string
		schedule string
		wantOK   bool
		wantDur  time.Duration
	}{
		{name: "daily", schedule: "daily", wantOK: true, wantDur: 24 * time.Hour},
		{name: "hourly", schedule: "@hourly", wantOK: true, wantDur: time.Hour},
		{name: "every duration", schedule: "every 6h", wantOK: true, wantDur: 6 * time.Hour},
		{name: "none", schedule: "", wantOK: false},
		{name: "invalid", schedule: "foo", wantOK: false},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := nextRunFromSchedule(tc.schedule, now)
			if ok != tc.wantOK {
				t.Fatalf("nextRunFromSchedule(%q) ok=%v, want %v", tc.schedule, ok, tc.wantOK)
			}
			if !tc.wantOK {
				return
			}
			if got.Sub(now) != tc.wantDur {
				t.Fatalf("nextRunFromSchedule(%q) duration=%s, want %s", tc.schedule, got.Sub(now), tc.wantDur)
			}
		})
	}
}
