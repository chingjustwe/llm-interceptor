package storage

import (
	"context"

	"github.com/chingjustwe/llm-interceptor/internal/types"
)

type Backend interface {
	SaveRequest(ctx context.Context, req *types.StoredRequest) error
	GetSessionRequests(ctx context.Context, sessionID string, limit, offset int) ([]types.StoredRequest, error)
	QueryRequests(ctx context.Context, filter types.RequestFilter) ([]types.StoredRequest, error)
	Close() error
}
