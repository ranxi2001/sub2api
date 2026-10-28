package repository

import (
	"context"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

func (r *channelRepository) ReadUpstreamOperations(ctx context.Context, f service.UpstreamOperationsFilter) (*service.UpstreamOperationsSnapshot, error) {
	return &service.UpstreamOperationsSnapshot{Scope: f, Usage: []byte(`{"summary":{"requests":0},"rows":[],"total":0}`)}, nil
}
