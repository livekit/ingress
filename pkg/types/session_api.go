package types

import (
	"context"
)

type SessionAPI interface {
	MediaStatsUpdater
	MediaStatGatherer

	GetProfileData(ctx context.Context, profileName string, timeout int, debug int) (b []byte, err error)
	GetPipelineDot(ctx context.Context) (string, error)
}
