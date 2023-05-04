package whip

import (
	"context"

	"github.com/tinyzimmer/go-gst/gst/app"

	"github.com/livekit/ingress/pkg/params"
	"github.com/livekit/ingress/pkg/types"
	"github.com/livekit/protocol/logger"
)

// TODO STUN & TURN
// TODO pion log level

const (
	WHIPAppSourceLabel   = "whipAppSrc"
	defaultUDPBufferSize = 16_777_216
)

type WHIPSource struct {
	params   *params.Params
	trackSrc map[types.StreamKind]*WHIPAppSource
}

func NewWHIPSource(p *params.Params) (*WHIPSource, error) {
	s := &WHIPSource{
		params:   p,
		trackSrc: make(map[types.StreamKind]*WHIPAppSource),
	}

	return s, nil
}

func (s *WHIPSource) Start(ctx context.Context) error {
	s.trackLock.Lock()
	defer s.trackLock.Unlock()

	for _, v := range s.trackSrc {
		err := v.Start()
		if err != nil {
			return err
		}
	}

	return nil
}

func (s *WHIPSource) Close() error {
	if s.pc == nil {
		return nil
	}

	s.pc.Close()

	return <-s.result
}

func (s *WHIPSource) GetSources(ctx context.Context) []*app.Source {
	for {
		s.trackLock.Lock()
		if len(s.trackSrc) >= s.expectedTrackCount {
			ret := make([]*app.Source, 0)

			for _, v := range s.trackSrc {
				ret = append(ret, v.GetAppSource())
			}

			s.trackLock.Unlock()
			return ret
		}
		s.trackLock.Unlock()

		select {
		case <-ctx.Done():
			logger.Infow("GetSources cancelled before all sources were ready")
			return nil
		case <-s.trackAddedChan:
			// continue
		}
	}
}
