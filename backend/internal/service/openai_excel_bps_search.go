package service

import (
	"bytes"
	"context"
	"fmt"
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
		return empty, err
	}
	req, err := newExcelBPSSearchRequest(ctx, body, token, accountID)
	if err != nil {
		return empty, fmt.Errorf("BPS search request could not be prepared")
	}
	resp, err := s.httpUpstream.Do(req, proxy, account.ID, account.Concurrency)
	if err != nil {
		if ctx.Err() != nil {
			return empty, ctx.Err()
		}
		return empty, fmt.Errorf("BPS search connection failed; request was not replayed")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return empty, fmt.Errorf("BPS search rejected with HTTP %d; request was not replayed", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (2<<20)+1))
	if err != nil {
		return empty, fmt.Errorf("BPS search result could not be read")
	}
	return basispoints.ParseSearchResult(raw)
}

func (s *OpenAIGatewayService) continueExcelBPSSearch(ctx context.Context, account *Account, body []byte, token, accountID, proxy string) (io.ReadCloser, error) {
	req, err := newExcelBPSRequest(ctx, body, token, accountID)
	if err != nil {
		return nil, fmt.Errorf("BPS search continuation could not be prepared")
	}
	resp, err := s.doPriorityObservedHTTP(req, proxy, account)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("BPS search continuation connection failed; request was not replayed")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("BPS search continuation rejected with HTTP %d; request was not replayed", resp.StatusCode)
	}
	s.UpdateCodexUsageSnapshotFromHeaders(ctx, account.ID, resp.Header)
	s.guardExcelBPSProgress(ctx, resp)
	return resp.Body, nil
}
