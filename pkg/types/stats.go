package types

import (
	"context"

	"github.com/livekit/ingress/pkg/ipc"
)

type MediaStatsUpdater interface {
	UpdateMediaStats(ctx context.Context, stats *ipc.MediaStats) error
}

type MediaStatGatherer interface {
	GatherStats(ctx context.Context) (*ipc.MediaStats, error)
}
