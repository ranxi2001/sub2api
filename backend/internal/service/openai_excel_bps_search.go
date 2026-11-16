package service

import (
	"bytes"
	"context"
	"io"
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/service/basispoints"
	"github.com/google/uuid"
)

var excelBPSSearchSlots = make(chan struct{}, 16)

func newExcelBPSSearchRequest(ctx context.Context, body []byte, token, accountID string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(WithHTTPUpstreamRedirectsDisabled(ctx), http.MethodPost, basispoints.SearchURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header = http.Header{"Authorization": {"Bearer " + token}, "Chatgpt-Account-Id": {accountID}, "Content-Type": {"application/json"}, "Accept": {"application/json"}, "User-Agent": {"Mozilla/5.0"}}
	return req, nil
}

func (s *OpenAIGatewayService) executeExcelBPSSearch(ctx context.Context, account *Account, plan *basispoints.SearchPlan, token, accountID, proxy, model, query string) (basispoints.SearchResult, error) {
	var empty basispoints.SearchResult
	select {
	case excelBPSSearchSlots <- struct{}{}:
		defer func() { <-excelBPSSearchSlots }()
	case <-ctx.Done():
		return empty, ctx.Err()
	}
	body, err := plan.SearchRequestBody(model, uuid.NewString(), query)
	if err != nil {
		return empty, basispoints.NewSearchFailure("search_http", "request_failed", 0)
	}
	req, err := newExcelBPSSearchRequest(ctx, body, token, accountID)
	if err != nil {
		return empty, basispoints.NewSearchFailure("search_http", "request_failed", 0)
	}
	resp, err := s.httpUpstream.Do(req, proxy, account.ID, account.Concurrency)
	if err != nil {
		if ctx.Err() != nil {
			return empty, ctx.Err()
		}
		return empty, basispoints.NewSearchFailure("search_http", "transport_failed", 0)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return empty, basispoints.NewSearchFailure("search_http", "http_rejected", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (2<<20)+1))
	if err != nil {
		return empty, basispoints.NewSearchFailure("search_result", "read_failed", resp.StatusCode)
	}
	result, err := basispoints.ParseSearchResult(raw)
	if err != nil {
		return empty, basispoints.NewSearchFailure("search_result", "parse_failed", resp.StatusCode)
	}
	return result, nil
}

func (s *OpenAIGatewayService) continueExcelBPSSearch(ctx context.Context, account *Account, body []byte, token, accountID, proxy string) (io.ReadCloser, error) {
	req, err := newExcelBPSRequest(ctx, body, token, accountID)
	if err != nil {
		return nil, basispoints.NewSearchFailure("model_continuation", "request_failed", 0)
	}
	resp, err := s.doPriorityObservedHTTP(req, proxy, account)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, basispoints.NewSearchFailure("model_continuation", "transport_failed", 0)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_ = resp.Body.Close()
		return nil, basispoints.NewSearchFailure("model_continuation", "http_rejected", resp.StatusCode)
	}
	s.UpdateCodexUsageSnapshotFromHeaders(ctx, account.ID, resp.Header)
	s.guardExcelBPSProgress(ctx, resp)
	return resp.Body, nil
}
