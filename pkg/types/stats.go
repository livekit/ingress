package types

import "context"

type MediaStatsUpdater interface {
	UpdateMediaStats(ctx context.Context, stats *MediaStats) error
}

type MediaStats map[string]interface{}
