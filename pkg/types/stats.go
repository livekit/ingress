package types

import (
	"context"

	"github.com/livekit/ingress/pkg/ipc"
)

type MediaStatsUpdater interface {
	UpdateMediaStats(ctx context.Context, stats *ipc.MediaStats) error
}
