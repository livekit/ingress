package types

type StreamKind string

const (
	Audio       StreamKind = "audio"
	Video       StreamKind = "video"
	Interleaved StreamKind = "interleaved"
	Unknown     StreamKind = "unknown"
)
