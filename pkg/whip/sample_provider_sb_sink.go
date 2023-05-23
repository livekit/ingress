package whip

import (
	"io"

	"github.com/frostbyte73/core"
	"github.com/livekit/ingress/pkg/lksdk_output"
	"github.com/livekit/ingress/pkg/types"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/psrpc"
	"github.com/pion/webrtc/v3"
	"github.com/pion/webrtc/v3/pkg/media"
)

type SDKMediaSink struct {
	writePLI func()

	readySamples chan *media.Sample
	fuse         core.Fuse
}

func NewSDKMediaSink(sdkOutput *lksdk_output.LKSDKOutput, track *webrtc.TrackRemote, writePLI func()) *SDKMediaSink {
	s := &SDKMediaSink{
		writePLI:     writePLI,
		readySamples: make(chan *media.Sample, 1),
		fuse:         core.NewFuse(),
	}

	kind := streamKindFromCodecType(track.Kind())
	mimeType := track.Codec().MimeType

	switch kind {
	case types.Audio:
		sdkOutput.AddAudioTrack(s, mimeType, false, false)
	case types.Video:
		layers := []*livekit.VideoLayer{
			&livekit.VideoLayer{Width: 1280, Height: 720},
		}
		sp := []lksdk_output.VideoSampleProvider{
			s,
		}

		sdkOutput.AddVideoTrack(sp, layers, mimeType)
	}

	return s
}

func (sp *SDKMediaSink) PushSample(s *media.Sample) error {
	select {
	case <-sp.fuse.Watch():
		return io.EOF
	case sp.readySamples <- s:
	}

	return nil
}

func (sp *SDKMediaSink) NextSample() (media.Sample, error) {
	select {
	case <-sp.fuse.Watch():
		return media.Sample{}, io.EOF
	case s := <-sp.readySamples:
		return *s, nil
	}
}

func (sp *SDKMediaSink) OnBind() error {
	return nil
}

func (sp *SDKMediaSink) OnUnbind() error {
	return nil
}

func (sp *SDKMediaSink) ForceKeyFrame() error {
	if sp.writePLI != nil {
		sp.writePLI()
	}

	return nil
}

func (sp *SDKMediaSink) SetWriter(w io.WriteCloser) error {
	return psrpc.Unimplemented
}

func (sp *SDKMediaSink) Close() {
	sp.fuse.Break()
}
