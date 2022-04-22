package media

// IngressPipeline manages the underlying GStreamer pipeline that transforms Input into elemental streams
// that are consumable
type IngressPipeline struct {
	input       *Input
	audioOutput *OutputStream
	videoOutput *OutputStream
}

func NewIngressPipeline() *IngressPipeline {
	return &IngressPipeline{}
}
