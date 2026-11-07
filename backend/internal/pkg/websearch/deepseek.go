package websearch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"
)

const (
	deepSeekSearchEndpoint = "https://api.deepseek.com/v1/tools/search"
	deepSeekMaxCount       = 50
	deepSeekMaxQueryLength = 128
)

// DeepSeekProvider uses DeepSeek's standalone Search API, without an LLM call.
// Proxy selection and timeouts are supplied by Manager through the HTTP client.
type DeepSeekProvider struct {
	apiKey     string
	httpClient *http.Client
}

func NewDeepSeekProvider(apiKey string, httpClient *http.Client) *DeepSeekProvider {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	// Keep bearer credentials and the query at the fixed API origin. Copy the
	// cached client so this policy does not affect Brave or Tavily.
	client := *httpClient
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &DeepSeekProvider{apiKey: apiKey, httpClient: &client}
}

func (d *DeepSeekProvider) Name() string { return ProviderTypeDeepSeek }

func (d *DeepSeekProvider) Search(ctx context.Context, req SearchRequest) (*SearchResponse, error) {
	// Count Unicode code points, rather than bytes, and never truncate queries:
	// truncation could remove a domain restriction or change the search meaning.
	if !utf8.ValidString(req.Query) || strings.TrimSpace(req.Query) == "" || utf8.RuneCountInString(req.Query) > deepSeekMaxQueryLength {
		return nil, fmt.Errorf("deepseek: query must contain 1 to %d Unicode characters and must not be blank", deepSeekMaxQueryLength)
	}
	count := req.MaxResults
	if count <= 0 {
		count = defaultMaxResults
	}
	if count > deepSeekMaxCount {
		count = deepSeekMaxCount
	}
	payload, err := json.Marshal(deepSeekRequest{Query: req.Query, Count: count, NeedContent: false})
	if err != nil {
		return nil, fmt.Errorf("deepseek: encode request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, deepSeekSearchEndpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("deepseek: build request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+d.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	resp, err := d.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("deepseek: request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	// Do not propagate upstream error bodies, which may echo credentials.
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("deepseek: status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize+1))
	if err != nil {
		return nil, fmt.Errorf("deepseek: read body: %w", err)
	}
	if len(body) > maxResponseSize {
		return nil, fmt.Errorf("deepseek: response exceeds size limit")
	}
	var raw deepSeekResponse
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("deepseek: decode response: invalid JSON or result fields")
	}
	results := make([]SearchResult, 0, len(raw.Results))
	for _, result := range raw.Results {
		if len(results) >= count {
			break
		}
		snippet := result.Summary
		if strings.TrimSpace(snippet) == "" {
			snippet = result.Snippet
		}
		results = append(results, SearchResult{URL: result.URL, Title: result.Title, Snippet: snippet, PageAge: result.Date})
	}
	return &SearchResponse{Results: results, Query: req.Query}, nil
}

type deepSeekRequest struct {
	Query       string `json:"query"`
	Count       int    `json:"count"`
	NeedContent bool   `json:"need_content"`
}

type deepSeekResponse struct {
	Results []struct {
		URL     string `json:"url"`
		Title   string `json:"title"`
		Snippet string `json:"snippet"`
		Summary string `json:"summary"`
		Date    string `json:"date"`
	} `json:"results"`
}
