package xai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

type capturedRequest struct {
	Method        string
	Path          string
	Authorization string
	ContentType   string
	Body          responsesRequest
}

func TestQuerySuccessBuildsRequestAndParsesOutput(t *testing.T) {
	t.Parallel()

	var captured capturedRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured.Method = r.Method
		captured.Path = r.URL.Path
		captured.Authorization = r.Header.Get("Authorization")
		captured.ContentType = r.Header.Get("Content-Type")

		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		if err := json.Unmarshal(body, &captured.Body); err != nil {
			t.Fatalf("unmarshal request body: %v", err)
		}

		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write([]byte(`{
			"id":"resp-1",
			"model":"grok-4",
			"output_text":"output_text from api",
			"output":[
				{
					"content":[
						{
							"text":"ignored fallback",
							"annotations":[
								{"type":"url_citation","url":"https://x.com/openai/status/1","title":"OpenAI Post"}
							]
						}
					]
				}
			]
		}`)); err != nil {
			t.Fatalf("write response: %v", err)
		}
	}))
	defer server.Close()

	client := NewClient(Config{
		BaseURL:    server.URL,
		APIKey:     "test-api-key",
		Model:      "grok-4",
		HTTPClient: server.Client(),
	})

	result, err := client.Query(context.Background(), "latest post", SearchOptions{
		AllowedXHandles:          []string{"openai"},
		ExcludedXHandles:         []string{"spam"},
		FromDate:                 "2026-02-01",
		ToDate:                   "2026-02-27",
		EnableImageUnderstanding: true,
		EnableVideoUnderstanding: false,
	})
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}

	if captured.Method != http.MethodPost {
		t.Fatalf("method = %q, want %q", captured.Method, http.MethodPost)
	}
	if captured.Path != "/responses" {
		t.Fatalf("path = %q, want /responses", captured.Path)
	}
	if captured.Authorization != "Bearer test-api-key" {
		t.Fatalf("authorization = %q, want %q", captured.Authorization, "Bearer test-api-key")
	}
	if captured.ContentType != "application/json" {
		t.Fatalf("content-type = %q, want application/json", captured.ContentType)
	}
	if captured.Body.Model != "grok-4" {
		t.Fatalf("request model = %q, want %q", captured.Body.Model, "grok-4")
	}
	if captured.Body.Input != "latest post" {
		t.Fatalf("request input = %q, want %q", captured.Body.Input, "latest post")
	}
	if len(captured.Body.Tools) != 1 {
		t.Fatalf("request tools length = %d, want 1", len(captured.Body.Tools))
	}
	tool := captured.Body.Tools[0]
	if tool.Type != "x_search" {
		t.Fatalf("tool.type = %q, want x_search", tool.Type)
	}
	if len(tool.AllowedXHandles) != 1 || tool.AllowedXHandles[0] != "openai" {
		t.Fatalf("tool.allowed_x_handles = %#v, want [openai]", tool.AllowedXHandles)
	}
	if len(tool.ExcludedXHandles) != 1 || tool.ExcludedXHandles[0] != "spam" {
		t.Fatalf("tool.excluded_x_handles = %#v, want [spam]", tool.ExcludedXHandles)
	}
	if tool.FromDate != "2026-02-01" {
		t.Fatalf("tool.from_date = %q, want %q", tool.FromDate, "2026-02-01")
	}
	if tool.ToDate != "2026-02-27" {
		t.Fatalf("tool.to_date = %q, want %q", tool.ToDate, "2026-02-27")
	}
	if !tool.EnableImageUnderstanding {
		t.Fatalf("tool.enable_image_understanding = false, want true")
	}
	if tool.EnableVideoUnderstanding {
		t.Fatalf("tool.enable_video_understanding = true, want false")
	}

	if result.Text != "output_text from api" {
		t.Fatalf("result text = %q, want %q", result.Text, "output_text from api")
	}
	if result.ResponseID != "resp-1" {
		t.Fatalf("result response id = %q, want %q", result.ResponseID, "resp-1")
	}
	if result.Model != "grok-4" {
		t.Fatalf("result model = %q, want %q", result.Model, "grok-4")
	}
	if len(result.Citations) != 1 {
		t.Fatalf("result citations length = %d, want 1", len(result.Citations))
	}
	if result.Citations[0].URL != "https://x.com/openai/status/1" {
		t.Fatalf("citation url = %q, want %q", result.Citations[0].URL, "https://x.com/openai/status/1")
	}
}

func TestQueryFallbackToOutputContentText(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write([]byte(`{
			"id":"resp-2",
			"model":"grok-3",
			"output":[
				{"content":[{"text":"first line"},{"text":"second line"}]},
				{"content":[{"text":"third line"}]}
			]
		}`)); err != nil {
			t.Fatalf("write response: %v", err)
		}
	}))
	defer server.Close()

	client := NewClient(Config{
		BaseURL:    server.URL,
		Model:      "grok-3",
		HTTPClient: server.Client(),
	})

	result, err := client.Query(context.Background(), "fallback please", SearchOptions{})
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}

	if result.Text != "first line\nsecond line\nthird line" {
		t.Fatalf("result text = %q, want %q", result.Text, "first line\nsecond line\nthird line")
	}
	if result.ResponseID != "resp-2" {
		t.Fatalf("result response id = %q, want %q", result.ResponseID, "resp-2")
	}
	if result.Model != "grok-3" {
		t.Fatalf("result model = %q, want %q", result.Model, "grok-3")
	}
}

func TestQueryReturnsBodyMessageOnAPIErrors(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		if _, err := w.Write([]byte(`{"error":"invalid x_search settings"}`)); err != nil {
			t.Fatalf("write response: %v", err)
		}
	}))
	defer server.Close()

	client := NewClient(Config{
		BaseURL:    server.URL,
		Model:      "grok-3",
		HTTPClient: server.Client(),
	})

	_, err := client.Query(context.Background(), "bad request", SearchOptions{})
	if err == nil {
		t.Fatal("Query() error = nil, want error")
	}
	if err.Error() != `{"error":"invalid x_search settings"}` {
		t.Fatalf("error = %q, want %q", err.Error(), `{"error":"invalid x_search settings"}`)
	}
}
