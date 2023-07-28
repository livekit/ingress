// Copyright 2023 LiveKit, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package whip

import (
	"context"
	"fmt"
	"io"

	"github.com/livekit/ingress/pkg/params"
	"github.com/livekit/ingress/pkg/types"
	"github.com/livekit/protocol/utils"
	"github.com/tinyzimmer/go-gst/gst/base"
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
		resourceId: p.State.ResourceId,
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
	errChans := make([]<-chan error, 0)
	for _, t := range s.trackSrc {
		errChans = append(errChans, t.Close())
	}

	for _, errChan := range errChans {
		err := <-errChan

		switch err {
		case nil, io.EOF:
			// success
		default:
			errs.AppendErr(err)
		}
	}

	return errs.ToError()
}

func (s *WHIPSource) GetSources(ctx context.Context) []*base.GstBaseSrc {
	ret := make([]*base.GstBaseSrc, 0, len(s.trackSrc))

	for _, t := range s.trackSrc {
		ret = append(ret, t.GetAppSource().GstBaseSrc)
	}

	return ret
}

func (s *WHIPSource) getRelayUrl(kind types.StreamKind) string {
	return fmt.Sprintf("%s/%s", s.params.RelayUrl, kind)
}
