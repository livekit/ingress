package whip

import (
	"io"
	"net"
	"sync"

	"github.com/frostbyte73/core"
	"github.com/livekit/ingress/pkg/utils"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/server-sdk-go/pkg/samplebuilder"
	"github.com/livekit/server-sdk-go/pkg/synchronizer"
	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v3"
)

type whipTrackHandler struct {
	remoteTrack *webrtc.TrackRemote
	receiver    *webrtc.RTPReceiver
	sb          *samplebuilder.SampleBuilder
	sync        *synchronizer.TrackSynchronizer
	writePLI    func(ssrc webrtc.SSRC)
	onRTCP      func(packet rtcp.Packet)

	firstPacket sync.Once
	mediaBuffer *utils.PrerollBuffer
	fuse        core.Fuse
	result      chan error
}

func newWHIPTrackHandler(
	track *webrtc.TrackRemote,
	receiver *webrtc.RTPReceiver,
	sync *synchronizer.TrackSynchronizer,
	writePLI func(ssrc webrtc.SSRC),
	onRTCP func(packet rtcp.Packet),
) (*whipTrackHandler, error) {
	t := &whipTrackHandler{
		remoteTrack: remoteTrack,
		receiver:    receiver,
		sync:        sync,
		writePLI:    writePLI,
		onRTCP:      onRTCP,
		fuse:        core.NewFuse(),
	}

	sb, err := t.createSampleBuilder()
	if err != nil {
		return nil, err
	}
	t.sb = sb

	return t, nil
}

func (t *whipTrackHandler) Start() error {
	t.result = make(chan error, 1)
	t.startRTPReceiver()
	if t.onRTCP != nil {
		t.startRTCPReceiver()
	}

	return nil
}

func (t *whipTrackHandler) Close() error {
	t.fuse.Break()

	return <-t.result
}

func (t *WHIPAppSource) startRTPReceiver() {
	go func() {
		var err error

		defer func() {
			t.appSrc.EndStream()

			t.result <- err
			close(t.result)
		}()

		logger.Infow("starting app source track reader", "trackID", t.remoteTrack.ID(), "kind", t.remoteTrack.Kind())

		if t.remoteTrack.Kind() == webrtc.RTPCodecTypeVideo && t.writePLI != nil {
			t.writePLI(t.remoteTrack.SSRC())
		}

		for {
			select {
			case <-t.fuse.Watch():
				logger.Debugw("stopping app source track reader", "trackID", t.remoteTrack.ID(), "kind", t.remoteTrack.Kind())
				return
			default:
				err = t.processRTPPacket()
				switch {
				case err == nil:
					// continue
				case err == io.EOF:
					err = nil
					return
				default:
					if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
						continue
					}

					logger.Warnw("error reading track", err, "trackID", err, t.remoteTrack.ID(), "kind", t.remoteTrack.Kind())
					return
				}
			}
		}

	}()
}
