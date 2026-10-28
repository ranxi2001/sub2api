package service

import (
	"context"
	"encoding/json"
	"time"
)

type UpstreamOperationsFilter struct {
	From       time.Time `json:"from"`
	Until      time.Time `json:"until"`
	Upstream   string    `json:"upstream"`
	AccountID  int64     `json:"account_id"`
	GroupID    int64     `json:"group_id"`
	Model      string    `json:"model"`
	ModelScope string    `json:"model_scope"`
	Dimension  string    `json:"dimension"`
	Sort       string    `json:"sort"`
	Direction  string    `json:"direction"`
	Page       int       `json:"page"`
	PageSize   int       `json:"page_size"`
}

type UpstreamOperationsSnapshot struct {
	Scope       UpstreamOperationsFilter `json:"scope"`
	QueriedAt   time.Time                `json:"queried_at"`
	Attribution string                   `json:"attribution"`
	Currency    string                   `json:"currency"`
	Usage       json.RawMessage          `json:"usage"`
	Quality     ChannelOperationsSection `json:"quality"`
	Errors      ChannelOperationsSection `json:"errors"`
}

type UpstreamOperationsRepository interface {
	ReadUpstreamOperations(context.Context, UpstreamOperationsFilter) (*UpstreamOperationsSnapshot, error)
}

func ValidateUpstreamOperationsFilter(f UpstreamOperationsFilter) error { return nil }
func (s *ChannelService) UpstreamOperations(ctx context.Context, f UpstreamOperationsFilter) (*UpstreamOperationsSnapshot, error) {
	return s.repo.(UpstreamOperationsRepository).ReadUpstreamOperations(ctx, f)
}

// ResolveOperationsUpstream returns a safe current configuration identity only.
func ResolveOperationsUpstream(a *Account) (string, string) { return "", "unresolved" }
