package xai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const defaultBaseURL = "https://api.x.ai/v1"

type Config struct {
	BaseURL    string
	APIKey     string
	Model      string
	HTTPClient *http.Client
}

type Client struct {
	baseURL    string
	apiKey     string
	model      string
	httpClient *http.Client
}

type SearchOptions struct {
	AllowedXHandles          []string
	ExcludedXHandles         []string
	FromDate                 string
	ToDate                   string
	EnableImageUnderstanding bool
	EnableVideoUnderstanding bool
}

type Citation struct {
	Type    string `json:"type,omitempty"`
	URL     string `json:"url,omitempty"`
	Title   string `json:"title,omitempty"`
	XHandle string `json:"x_handle,omitempty"`
	PostID  string `json:"post_id,omitempty"`
}

type QueryResult struct {
	Text       string
	Citations  []Citation
	ResponseID string
	Model      string
}

func NewClient(cfg Config) *Client {
	baseURL := strings.TrimSpace(cfg.BaseURL)
	if baseURL == "" {
		baseURL = defaultBaseURL
	}

	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}

	return &Client{
		baseURL:    baseURL,
		apiKey:     strings.TrimSpace(cfg.APIKey),
		model:      strings.TrimSpace(cfg.Model),
		httpClient: httpClient,
	}
}

func (c *Client) Query(ctx context.Context, query string, options SearchOptions) (QueryResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	query = strings.TrimSpace(query)
	if query == "" {
		return QueryResult{}, errors.New("query is required")
	}
	if c.model == "" {
		return QueryResult{}, errors.New("model is required")
	}

	reqBody := responsesRequest{
		Model: c.model,
		Input: query,
		Tools: []xSearchTool{
			{
				Type:                     "x_search",
				AllowedXHandles:          append([]string(nil), options.AllowedXHandles...),
				ExcludedXHandles:         append([]string(nil), options.ExcludedXHandles...),
				FromDate:                 strings.TrimSpace(options.FromDate),
				ToDate:                   strings.TrimSpace(options.ToDate),
				EnableImageUnderstanding: options.EnableImageUnderstanding,
				EnableVideoUnderstanding: options.EnableVideoUnderstanding,
			},
		},
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return QueryResult{}, fmt.Errorf("marshal responses request: %w", err)
	}

	endpoint := strings.TrimRight(c.baseURL, "/") + "/responses"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return QueryResult{}, fmt.Errorf("build responses request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return QueryResult{}, fmt.Errorf("post responses request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return QueryResult{}, fmt.Errorf("read responses body: %w", err)
	}

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		message := strings.TrimSpace(string(respBody))
		if message == "" {
			message = resp.Status
		}
		return QueryResult{}, errors.New(message)
	}

	var decoded responsesResponse
	if err := json.Unmarshal(respBody, &decoded); err != nil {
		return QueryResult{}, fmt.Errorf("decode responses body: %w", err)
	}

	text := strings.TrimSpace(decoded.OutputText)
	if text == "" {
		text = restoreOutputText(decoded.Output)
	}

	return QueryResult{
		Text:       text,
		Citations:  collectCitations(decoded),
		ResponseID: strings.TrimSpace(decoded.ID),
		Model:      strings.TrimSpace(decoded.Model),
	}, nil
}

type responsesRequest struct {
	Model string        `json:"model"`
	Input string        `json:"input"`
	Tools []xSearchTool `json:"tools"`
}

type xSearchTool struct {
	Type                     string   `json:"type"`
	AllowedXHandles          []string `json:"allowed_x_handles,omitempty"`
	ExcludedXHandles         []string `json:"excluded_x_handles,omitempty"`
	FromDate                 string   `json:"from_date,omitempty"`
	ToDate                   string   `json:"to_date,omitempty"`
	EnableImageUnderstanding bool     `json:"enable_image_understanding"`
	EnableVideoUnderstanding bool     `json:"enable_video_understanding"`
}

type responsesResponse struct {
	ID         string           `json:"id"`
	Model      string           `json:"model"`
	OutputText string           `json:"output_text"`
	Citations  []Citation       `json:"citations"`
	Output     []responseOutput `json:"output"`
}

type responseOutput struct {
	Content []responseContent `json:"content"`
}

type responseContent struct {
	Text        string     `json:"text"`
	Citations   []Citation `json:"citations"`
	Annotations []Citation `json:"annotations"`
}

func restoreOutputText(outputs []responseOutput) string {
	parts := make([]string, 0, len(outputs))
	for _, output := range outputs {
		for _, content := range output.Content {
			text := strings.TrimSpace(content.Text)
			if text != "" {
				parts = append(parts, text)
			}
		}
	}
	return strings.Join(parts, "\n")
}

func collectCitations(resp responsesResponse) []Citation {
	out := make([]Citation, 0, len(resp.Citations))
	seen := map[string]struct{}{}

	appendCitation := func(c Citation) {
		if isEmptyCitation(c) {
			return
		}
		key := citationKey(c)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		out = append(out, c)
	}

	for _, citation := range resp.Citations {
		appendCitation(citation)
	}
	for _, output := range resp.Output {
		for _, content := range output.Content {
			for _, citation := range content.Citations {
				appendCitation(citation)
			}
			for _, citation := range content.Annotations {
				appendCitation(citation)
			}
		}
	}
	return out
}

func isEmptyCitation(c Citation) bool {
	return strings.TrimSpace(c.Type) == "" &&
		strings.TrimSpace(c.URL) == "" &&
		strings.TrimSpace(c.Title) == "" &&
		strings.TrimSpace(c.XHandle) == "" &&
		strings.TrimSpace(c.PostID) == ""
}

func citationKey(c Citation) string {
	return strings.TrimSpace(c.Type) + "|" +
		strings.TrimSpace(c.URL) + "|" +
		strings.TrimSpace(c.Title) + "|" +
		strings.TrimSpace(c.XHandle) + "|" +
		strings.TrimSpace(c.PostID)
}
