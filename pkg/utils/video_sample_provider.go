package utils

import (
	"io"

	"github.com/frostbyte73/core"
	"github.com/livekit/server-sdk-go/pkg/samplebuilder"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v3/pkg/media"
)

type SBSampleProvider struct {
	sb *samplebuilder.SampleBuilder

	readySamples chan *media.Sample
	fuse         core.Fuse
}

func NewSBSampleProvider(sb *samplebuilder.SampleBuilder) *SBSampleProvider {
	return &SBSampleProvider{
		sb:           sb,
		readySamples: make(chan *media.Sample, 1),
		fuse:         core.NewFuse(),
	}
}

func (sp *SBSampleProvider) Push(pkt *rtp.Packet) error {
	sp.sb.Push(pkt)

	for {
		s, _ := sp.sb.PopWithTimestamp()
		if s == nil {
			return nil
		}

		select {
		case <-sp.fuse.Watch():
			return io.EOF
		case sp.readySamples <- s:
		}
	}
}

func (sp *SBSampleProvider) NextSample() (media.Sample, error) {
	select {
	case <-sp.fuse.Watch():
		return media.Sample{}, io.EOF
	case s := <-sp.readySamples:
		return *s, nil
	}
}

func (sp *SBSampleProvider) Close() {
	sp.fuse.Break()
}
