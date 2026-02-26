package memory

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	defaultQueryLimit = 20
	defaultTaskStatus = "active"
)

type Store struct {
	root string
	mu   sync.Mutex
}

type UserNoteInput struct {
	UserID string
	Note   string
	Source string
}

type ChannelIntentInput struct {
	ChannelID string
	Intent    string
	Policy    string
}

type UpsertTaskInput struct {
	TaskID       string
	Title        string
	Instructions string
	ChannelID    string
	Schedule     string
	NextRunAt    time.Time
	Status       string
}

type Task struct {
	TaskID       string
	Title        string
	Instructions string
	ChannelID    string
	Schedule     string
	NextRunAt    time.Time
	LastRunAt    time.Time
	Status       string
	UpdatedAt    time.Time
}

type QueryInput struct {
	Keyword string
	Limit   int
}

type QueryResult struct {
	Path    string
	Excerpt string
}

type taskFrontMatter struct {
	TaskID       string `yaml:"task_id"`
	Title        string `yaml:"title"`
	Instructions string `yaml:"instructions"`
	ChannelID    string `yaml:"channel_id"`
	Schedule     string `yaml:"schedule"`
	NextRunAt    string `yaml:"next_run_at"`
	LastRunAt    string `yaml:"last_run_at"`
	Status       string `yaml:"status"`
	UpdatedAt    string `yaml:"updated_at"`
}

func NewStore(rootDir string) (*Store, error) {
	root := strings.TrimSpace(rootDir)
	if root == "" {
		return nil, errors.New("memory root dir is required")
	}
	store := &Store{root: filepath.Clean(root)}
	if err := store.ensureDirectories(); err != nil {
		return nil, err
	}
	if err := store.ensureIndex(); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *Store) Root() string {
	return s.root
}

func (s *Store) UpsertUserNote(_ context.Context, input UserNoteInput) (string, error) {
	if strings.TrimSpace(input.UserID) == "" {
		return "", errors.New("user_id is required")
	}
	if strings.TrimSpace(input.Note) == "" {
		return "", errors.New("note is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	path := s.userPath(input.UserID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("create user note directory: %w", err)
	}

	body, _ := os.ReadFile(path)
	if len(body) == 0 {
		body = []byte(fmt.Sprintf("# User %s\n\n## Notes\n", input.UserID))
	}
	entry := fmt.Sprintf("- %s: %s", now.Format(time.RFC3339), strings.TrimSpace(input.Note))
	if source := strings.TrimSpace(input.Source); source != "" {
		entry += fmt.Sprintf(" (source: %s)", source)
	}
	entry += "\n"

	updated := append(body, []byte(entry)...)
	if err := os.WriteFile(path, updated, 0o644); err != nil {
		return "", fmt.Errorf("write user note: %w", err)
	}
	if err := s.updateIndexLocked(now); err != nil {
		return "", err
	}
	return path, nil
}

func (s *Store) UpsertChannelIntent(_ context.Context, input ChannelIntentInput) (string, error) {
	if strings.TrimSpace(input.ChannelID) == "" {
		return "", errors.New("channel_id is required")
	}
	if strings.TrimSpace(input.Intent) == "" {
		return "", errors.New("intent is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	path := s.channelPath(input.ChannelID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("create channel note directory: %w", err)
	}

	content := strings.Builder{}
	content.WriteString(fmt.Sprintf("# Channel %s\n\n", input.ChannelID))
	content.WriteString(fmt.Sprintf("Updated: %s\n\n", now.Format(time.RFC3339)))
	content.WriteString("## Intent\n")
	content.WriteString(strings.TrimSpace(input.Intent) + "\n")
	if policy := strings.TrimSpace(input.Policy); policy != "" {
		content.WriteString("\n## Policy\n")
		content.WriteString(policy + "\n")
	}

	if err := os.WriteFile(path, []byte(content.String()), 0o644); err != nil {
		return "", fmt.Errorf("write channel intent: %w", err)
	}
	if err := s.updateIndexLocked(now); err != nil {
		return "", err
	}
	return path, nil
}

func (s *Store) UpsertTask(_ context.Context, input UpsertTaskInput) (Task, error) {
	if strings.TrimSpace(input.TaskID) == "" {
		return Task{}, errors.New("task_id is required")
	}
	if strings.TrimSpace(input.Title) == "" {
		return Task{}, errors.New("title is required")
	}
	if strings.TrimSpace(input.Instructions) == "" {
		return Task{}, errors.New("instructions is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	task, err := s.readTaskLocked(input.TaskID)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return Task{}, err
	}

	now := time.Now().UTC()
	task.TaskID = strings.TrimSpace(input.TaskID)
	task.Title = strings.TrimSpace(input.Title)
	task.Instructions = strings.TrimSpace(input.Instructions)
	task.ChannelID = strings.TrimSpace(input.ChannelID)
	task.Schedule = strings.TrimSpace(input.Schedule)
	if !input.NextRunAt.IsZero() {
		task.NextRunAt = input.NextRunAt.UTC()
	}
	if strings.TrimSpace(input.Status) != "" {
		task.Status = strings.TrimSpace(input.Status)
	}
	if task.Status == "" {
		task.Status = defaultTaskStatus
	}

	if task.NextRunAt.IsZero() {
		if next, ok := nextRunFromSchedule(task.Schedule, now); ok {
			task.NextRunAt = next
		}
	}
	task.UpdatedAt = now

	if err := s.writeTaskLocked(task); err != nil {
		return Task{}, err
	}
	if err := s.updateIndexLocked(now); err != nil {
		return Task{}, err
	}
	return task, nil
}

func (s *Store) Query(_ context.Context, input QueryInput) ([]QueryResult, error) {
	keyword := strings.TrimSpace(input.Keyword)
	if keyword == "" {
		return nil, errors.New("keyword is required")
	}
	limit := input.Limit
	if limit <= 0 {
		limit = defaultQueryLimit
	}

	matches := make([]QueryResult, 0, limit)
	lowered := strings.ToLower(keyword)
	err := filepath.WalkDir(s.root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		if filepath.Ext(path) != ".md" {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		text := string(body)
		idx := strings.Index(strings.ToLower(text), lowered)
		if idx < 0 {
			return nil
		}
		excerpt := snippetAt(text, idx, len(keyword))
		rel, err := filepath.Rel(s.root, path)
		if err != nil {
			rel = path
		}
		matches = append(matches, QueryResult{Path: rel, Excerpt: excerpt})
		if len(matches) >= limit {
			return fs.SkipAll
		}
		return nil
	})
	if err != nil && !errors.Is(err, fs.SkipAll) {
		return nil, fmt.Errorf("query memory: %w", err)
	}
	return matches, nil
}

func (s *Store) ClaimDueTasks(_ context.Context, now time.Time, limit int) ([]Task, error) {
	now = now.UTC()
	if limit <= 0 {
		limit = defaultQueryLimit
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	tasks, err := s.listTasksLocked()
	if err != nil {
		return nil, err
	}
	dues := make([]Task, 0, limit)
	for _, task := range tasks {
		if len(dues) >= limit {
			break
		}
		if !task.NextRunAt.IsZero() && task.NextRunAt.After(now) {
			continue
		}
		if strings.EqualFold(task.Status, "done") || strings.EqualFold(task.Status, "archived") {
			continue
		}

		task.LastRunAt = now
		if next, ok := nextRunFromSchedule(task.Schedule, now); ok {
			task.NextRunAt = next
		} else {
			task.NextRunAt = time.Time{}
			task.Status = "done"
		}
		task.UpdatedAt = now

		if err := s.writeTaskLocked(task); err != nil {
			return nil, err
		}
		dues = append(dues, task)
	}

	if len(dues) > 0 {
		if err := s.updateIndexLocked(now); err != nil {
			return nil, err
		}
	}
	return dues, nil
}

func (s *Store) ensureDirectories() error {
	for _, p := range []string{s.root, s.usersDir(), s.channelsDir(), s.tasksDir()} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			return fmt.Errorf("create memory directory %s: %w", p, err)
		}
	}
	return nil
}

func (s *Store) ensureIndex() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.updateIndexLocked(time.Now().UTC())
}

func (s *Store) updateIndexLocked(now time.Time) error {
	indexPath := filepath.Join(s.root, "index.md")
	content := strings.Builder{}
	content.WriteString("# Memory Index\n\n")
	content.WriteString(fmt.Sprintf("Updated: %s\n\n", now.Format(time.RFC3339)))
	content.WriteString("- users/: user specific notes\n")
	content.WriteString("- channels/: channel intent and rules\n")
	content.WriteString("- tasks/: recurring or one-shot tasks\n")
	return os.WriteFile(indexPath, []byte(content.String()), 0o644)
}

func (s *Store) listTasksLocked() ([]Task, error) {
	entries, err := os.ReadDir(s.tasksDir())
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read tasks dir: %w", err)
	}
	out := make([]Task, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".md" {
			continue
		}
		taskID := strings.TrimSuffix(entry.Name(), ".md")
		task, err := s.readTaskLocked(taskID)
		if err != nil {
			continue
		}
		out = append(out, task)
	}
	sort.Slice(out, func(i, j int) bool {
		a := out[i].NextRunAt
		b := out[j].NextRunAt
		switch {
		case a.IsZero() && b.IsZero():
			return out[i].TaskID < out[j].TaskID
		case a.IsZero():
			return false
		case b.IsZero():
			return true
		default:
			return a.Before(b)
		}
	})
	return out, nil
}

func (s *Store) readTaskLocked(taskID string) (Task, error) {
	path := s.taskPath(taskID)
	body, err := os.ReadFile(path)
	if err != nil {
		return Task{}, err
	}
	return decodeTask(string(body))
}

func (s *Store) writeTaskLocked(task Task) error {
	if err := os.MkdirAll(s.tasksDir(), 0o755); err != nil {
		return fmt.Errorf("create tasks directory: %w", err)
	}
	path := s.taskPath(task.TaskID)
	body, err := encodeTask(task)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return fmt.Errorf("write task %s: %w", task.TaskID, err)
	}
	return nil
}

func encodeTask(task Task) (string, error) {
	meta := taskFrontMatter{
		TaskID:       task.TaskID,
		Title:        task.Title,
		Instructions: task.Instructions,
		ChannelID:    task.ChannelID,
		Schedule:     task.Schedule,
		Status:       task.Status,
		UpdatedAt:    task.UpdatedAt.UTC().Format(time.RFC3339),
	}
	if !task.NextRunAt.IsZero() {
		meta.NextRunAt = task.NextRunAt.UTC().Format(time.RFC3339)
	}
	if !task.LastRunAt.IsZero() {
		meta.LastRunAt = task.LastRunAt.UTC().Format(time.RFC3339)
	}
	metaYAML, err := yaml.Marshal(meta)
	if err != nil {
		return "", fmt.Errorf("marshal task front matter: %w", err)
	}
	content := strings.Builder{}
	content.WriteString("---\n")
	content.Write(metaYAML)
	content.WriteString("---\n\n")
	content.WriteString(fmt.Sprintf("# Task %s\n\n", task.Title))
	content.WriteString(task.Instructions)
	content.WriteString("\n")
	return content.String(), nil
}

func decodeTask(body string) (Task, error) {
	parts := strings.SplitN(body, "---\n", 3)
	if len(parts) < 3 {
		return Task{}, errors.New("invalid task markdown front matter")
	}
	metaRaw := parts[1]
	var meta taskFrontMatter
	if err := yaml.Unmarshal([]byte(metaRaw), &meta); err != nil {
		return Task{}, fmt.Errorf("unmarshal task front matter: %w", err)
	}

	task := Task{
		TaskID:       strings.TrimSpace(meta.TaskID),
		Title:        strings.TrimSpace(meta.Title),
		Instructions: strings.TrimSpace(meta.Instructions),
		ChannelID:    strings.TrimSpace(meta.ChannelID),
		Schedule:     strings.TrimSpace(meta.Schedule),
		Status:       strings.TrimSpace(meta.Status),
	}
	if task.Status == "" {
		task.Status = defaultTaskStatus
	}
	if parsed, ok := parseTime(meta.NextRunAt); ok {
		task.NextRunAt = parsed
	}
	if parsed, ok := parseTime(meta.LastRunAt); ok {
		task.LastRunAt = parsed
	}
	if parsed, ok := parseTime(meta.UpdatedAt); ok {
		task.UpdatedAt = parsed
	}
	return task, nil
}

func parseTime(raw string) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, false
	}
	return t.UTC(), true
}

func nextRunFromSchedule(schedule string, now time.Time) (time.Time, bool) {
	s := strings.TrimSpace(strings.ToLower(schedule))
	switch s {
	case "":
		return time.Time{}, false
	case "daily", "every_day", "every 1d", "@daily":
		return now.Add(24 * time.Hour), true
	case "hourly", "every_hour", "every 1h", "@hourly":
		return now.Add(time.Hour), true
	case "weekly", "every_week", "every 7d", "@weekly":
		return now.Add(7 * 24 * time.Hour), true
	}

	if strings.HasPrefix(s, "every ") {
		d, err := time.ParseDuration(strings.TrimPrefix(s, "every "))
		if err == nil && d > 0 {
			return now.Add(d), true
		}
	}
	if strings.HasPrefix(s, "@every ") {
		d, err := time.ParseDuration(strings.TrimPrefix(s, "@every "))
		if err == nil && d > 0 {
			return now.Add(d), true
		}
	}
	return time.Time{}, false
}

func snippetAt(text string, idx int, width int) string {
	const side = 120
	start := idx - side
	if start < 0 {
		start = 0
	}
	end := idx + width + side
	if end > len(text) {
		end = len(text)
	}
	snippet := strings.TrimSpace(text[start:end])
	return strings.ReplaceAll(snippet, "\n", " ")
}

func (s *Store) usersDir() string {
	return filepath.Join(s.root, "users")
}

func (s *Store) channelsDir() string {
	return filepath.Join(s.root, "channels")
}

func (s *Store) tasksDir() string {
	return filepath.Join(s.root, "tasks")
}

func (s *Store) userPath(userID string) string {
	return filepath.Join(s.usersDir(), sanitizeID(userID)+".md")
}

func (s *Store) channelPath(channelID string) string {
	return filepath.Join(s.channelsDir(), sanitizeID(channelID)+".md")
}

func (s *Store) taskPath(taskID string) string {
	return filepath.Join(s.tasksDir(), sanitizeID(taskID)+".md")
}

func sanitizeID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unknown"
	}
	replacer := strings.NewReplacer("/", "_", "\\", "_", " ", "_", "\t", "_", "\n", "_")
	return replacer.Replace(value)
}
