package whip

import (
	"context"
	"fmt"
	"io"

	"github.com/tinyzimmer/go-gst/gst/app"

	"github.com/livekit/ingress/pkg/params"
	"github.com/livekit/ingress/pkg/types"
	"github.com/livekit/protocol/utils"
)

// TODO STUN & TURN
// TODO pion log level

const (
	WHIPAppSourceLabel   = "whipAppSrc"
	defaultUDPBufferSize = 16_777_216
)

type WHIPSource struct {
	params     *params.Params
	resourceId string
	trackSrc   map[types.StreamKind]*whipAppSource
}

func NewWHIPRelaySource(ctx context.Context, p *params.Params) (*WHIPSource, error) {
	s := &WHIPSource{
		params:     p,
		trackSrc:   make(map[types.StreamKind]*whipAppSource),
		resourceId: p.ExtraParams.(*params.WhipExtraParams).ResourceId,
	}

	mimeTypes := s.params.ExtraParams.(*params.WhipExtraParams).MimeTypes
	for k, v := range mimeTypes {
		relayUrl := s.getRelayUrl(k)
		t, err := NewWHIPAppSource(ctx, s.resourceId, k, v, relayUrl)
		if err != nil {
			return nil, err
		}

		s.trackSrc[k] = t
	}

	return s, nil
}

func (s *WHIPSource) Start(ctx context.Context) error {
	for _, t := range s.trackSrc {
		err := t.Start(ctx)
		if err != nil {
			return err
		}
	}

	return nil
}

func (s *WHIPSource) Close() error {
	var errs utils.ErrArray
	for _, t := range s.trackSrc {
		err := t.Close()
		switch err {
		case nil, io.EOF:
			// success
		default:
			errs.AppendErr(err)
		}
	}

	return errs.ToError()
}

func (s *WHIPSource) GetSources(ctx context.Context) []*app.Source {
	ret := make([]*app.Source, 0, len(s.trackSrc))

	for _, t := range s.trackSrc {
		ret = append(ret, t.GetAppSource())
	}

	return ret
}

func (s *WHIPSource) getRelayUrl(kind types.StreamKind) string {
	return fmt.Sprintf("%s/%s", s.params.RelayUrl, kind)
}
