package types

type StreamKind string

const (
	Audio       StreamKind = "audio"
	Video                  = "video"
	Interleaved            = "interleaved"
	Unknown                = "unknown"
)
