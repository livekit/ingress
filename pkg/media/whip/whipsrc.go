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
	"sync/atomic"
	"time"

	"github.com/go-gst/go-gst/gst"
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
	params      *params.Params
	resourceId  string
	startOffset atomic.Int64
	trackSrc    map[types.StreamKind]*whipAppSource
}

func NewWHIPRelaySource(ctx context.Context, p *params.Params) (*WHIPSource, error) {
	s := &WHIPSource{
		params:     p,
		trackSrc:   make(map[types.StreamKind]*whipAppSource),
		resourceId: p.State.ResourceId,
	}

	s.startOffset.Store(-1)

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

func (s *WHIPSource) Start(ctx context.Context, onClose func()) error {
	for _, t := range s.trackSrc {
		err := t.Start(ctx, s.getCorrctedTimestamp, onClose)
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

func (s *WHIPSource) GetSources() []*gst.Element {
	ret := make([]*gst.Element, 0, len(s.trackSrc))

	for _, t := range s.trackSrc {
		ret = append(ret, t.GetAppSource().Element)
	}

	return ret
}

func (s *WHIPSource) ValidateCaps(*gst.Caps) error {
	return nil
}

func (s *WHIPSource) getRelayUrl(kind types.StreamKind) string {
	return fmt.Sprintf("%s/%s?token=%s", s.params.RelayUrl, kind, s.params.RelayToken)
}

func (s *WHIPSource) getCorrctedTimestamp(ts time.Duration) time.Duration {
	s.startOffset.CompareAndSwap(-1, int64(ts))

	return ts - time.Duration(s.startOffset.Load())
}
