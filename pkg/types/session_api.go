package types

import "context"

type SessionAPI interface {
	GetProfileData(ctx context.Context, profileName string, timeout int, debug int) (b []byte, err error)
	GetPipelineDot(ctx context.Context) (string, error)
	UpdateMediaStats(ctx context.Context, stats *MediaStats) error
}

type MediaStats struct {
	AudioAverageBitrate *uint32
	AudioCurrentBitrate *uint32
	VideoAverageBitrate *uint32
	VideoCurrentBitrate *uint32
}
