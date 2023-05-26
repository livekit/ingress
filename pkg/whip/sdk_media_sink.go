package whip

import (
	"io"
	"strings"
	"time"

	"github.com/Eyevinn/mp4ff/avc"
	"github.com/frostbyte73/core"
	"github.com/pion/webrtc/v3"
	"github.com/pion/webrtc/v3/pkg/media"

	"github.com/livekit/ingress/pkg/errors"
	"github.com/livekit/ingress/pkg/lksdk_output"
	"github.com/livekit/ingress/pkg/types"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/psrpc"
)

var (
	ErrParamsUnavailable = psrpc.NewErrorf(psrpc.InvalidArgument, "codec parameters unavailable in sample")
)

type SDKMediaSink struct {
	logger    logger.Logger
	writePLI  func()
	track     *webrtc.TrackRemote
	sdkOutput *lksdk_output.LKSDKOutput

	readySamples     chan *media.Sample
	fuse             core.Fuse
	trackInitialized bool
}

func NewSDKMediaSink(l logger.Logger, sdkOutput *lksdk_output.LKSDKOutput, track *webrtc.TrackRemote, writePLI func()) *SDKMediaSink {
	s := &SDKMediaSink{
		logger:       l,
		writePLI:     writePLI,
		track:        track,
		sdkOutput:    sdkOutput,
		readySamples: make(chan *media.Sample, 1),
		fuse:         core.NewFuse(),
	}

	return s
}

func (sp *SDKMediaSink) PushSample(s *media.Sample, ts time.Duration) error {
	if sp.fuse.IsBroken() {
		return io.EOF
	}

	err := sp.ensureTrackInitialized(s)
	if err != nil {
		return err
	}
	if !sp.trackInitialized {
		// Drop the sample
		return nil
	}

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

func (sp *SDKMediaSink) ensureTrackInitialized(s *media.Sample) error {
	if sp.trackInitialized {
		return nil
	}

	kind := streamKindFromCodecType(sp.track.Kind())
	mimeType := sp.track.Codec().MimeType

	switch kind {
	case types.Audio:
		stereo := parseAudioFmtp(sp.track.Codec().SDPFmtpLine)

		sp.logger.Infow("adding audio track", "stereo", stereo, "codec", mimeType)
		sp.sdkOutput.AddAudioTrack(sp, mimeType, false, stereo)
	case types.Video:
		w, h, err := getVideoParams(mimeType, s)
		switch err {
		case nil:
			// continue
		case ErrParamsUnavailable:
			return nil
		default:
			return err
		}

		// TODO extract proper dimensions from stream
		layers := []*livekit.VideoLayer{
			&livekit.VideoLayer{Width: uint32(w), Height: uint32(h), Quality: livekit.VideoQuality_HIGH},
		}
		s := []lksdk_output.VideoSampleProvider{
			sp,
		}

		sp.logger.Infow("adding video track", "width", w, "height", h, "codec", mimeType)
		sp.sdkOutput.AddVideoTrack(s, layers, mimeType)
	}

	sp.trackInitialized = true

	return nil
}

func parseAudioFmtp(audioFmtp string) bool {
	return strings.Index(audioFmtp, "sprop-stereo=1") >= 0
}

func getVideoParams(mimeType string, s *media.Sample) (uint, uint, error) {
	switch strings.ToLower(mimeType) {
	case strings.ToLower(webrtc.MimeTypeH264):
		return getH264VideoParams(s)
	default:
		return 0, 0, errors.ErrUnsupportedDecodeFormat
	}
}

func getH264VideoParams(s *media.Sample) (uint, uint, error) {
	if !avc.HasParameterSets(s.Data) {
		return 0, 0, ErrParamsUnavailable
	}

	spss, _ := avc.GetParameterSetsFromByteStream(s.Data)
	if len(spss) == 0 {
		// Should not happen
		return 0, 0, ErrParamsUnavailable
	}

	sps, err := avc.ParseSPSNALUnit(spss[0], false)
	if err != nil {
		return 0, 0, err
	}

	return sps.Width, sps.Height, nil
}
