package types

import "context"

type SessionAPI interface {
	GetProfileData(ctx context.Context, profileName string, timeout int, debug int) (b []byte, err error)
	UpdateMediaStats(stats *MediaStats) error
}

type MediaStats struct {
	AudioAverageBitrate *int
	AudioCurrentBitrate *int
	VideoAverageBitrate *int
	VideoCurrentBitrate *int
}
