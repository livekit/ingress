package whip

import (
	"context"

	"github.com/livekit/ingress/pkg/params"
	"github.com/tinyzimmer/go-gst/gst/app"
)

const (
	// TODO: 2 for audio and video
	WhipAppSource = "whipAppSrc"
)

type WHIPSource struct {
	params *params.Params
}

func NewWHIPSource(ctx context.Context, p *params.Params) (*WHIPSource, error) {
	s := &WHIPSource{
		params: p,
	}

	return s, err
}

func (s *WHIPSource) Start(ctx context.Context) error {
	return nil
}

func (s *WHIPSource) Close() error {
	return nil
}

func (s *WHIPSource) GetSource() *app.Source {
	return nil
}
