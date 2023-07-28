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

package rtmp

import (
	"context"
	"io"
	"net/http"

	"github.com/tinyzimmer/go-gst/gst"
	"github.com/tinyzimmer/go-gst/gst/app"
	"github.com/tinyzimmer/go-gst/gst/base"
	"go.uber.org/atomic"

	"github.com/livekit/ingress/pkg/errors"
	"github.com/livekit/ingress/pkg/params"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/tracer"
)

const (
	FlvAppSource = "flvAppSrc"
)

type RTMPRelaySource struct {
	params *params.Params

	flvSrc *app.Source
	writer *appSrcWriter
	result chan error
}

func NewRTMPRelaySource(ctx context.Context, p *params.Params) (*RTMPRelaySource, error) {
	ctx, span := tracer.Start(ctx, "RTMPRelaySource.New")
	defer span.End()

	s := &RTMPRelaySource{
		params: p,
	}

	elem, err := gst.NewElementWithName("appsrc", FlvAppSource)
	if err != nil {
		logger.Errorw("could not create appsrc", err)
		return nil, err
	}
	if err = elem.SetProperty("caps", gst.NewCapsFromString("video/x-flv")); err != nil {
		return nil, err
	}
	if err = elem.SetProperty("is-live", true); err != nil {
		return nil, err
	}
	elem.SetArg("format", "time")

	s.flvSrc = app.SrcFromElement(elem)
	s.writer = newAppSrcWriter(s.flvSrc)

	return s, nil
}

func (s *RTMPRelaySource) Start(ctx context.Context) error {
	ctx, span := tracer.Start(ctx, "RTMPRelaySource.Start")
	defer span.End()

	s.result = make(chan error, 1)

	resp, err := http.Get(s.params.RelayUrl)
	switch {
	case err != nil:
		return err
	case resp != nil && (resp.StatusCode < 200 || resp.StatusCode >= 400):
		return errors.ErrHttpRelayFailure(resp.StatusCode)
	}

	go func() {
		defer resp.Body.Close()

		_, err := io.Copy(s.writer, resp.Body)
		switch err {
		case nil, io.EOF:
			err = nil
		default:
			logger.Errorw("error while copying media from relay", err)
		}

		s.flvSrc.EndStream()

		s.result <- err
		close(s.result)
	}()

	return nil
}

func (s *RTMPRelaySource) Close() error {
	s.writer.Close()
	return <-s.result
}

func (s *RTMPRelaySource) GetSources(ctx context.Context) []*base.GstBaseSrc {
	return []*base.GstBaseSrc{s.flvSrc.GstBaseSrc}
}

type appSrcWriter struct {
	appSrc *app.Source
	eos    *atomic.Bool
}

func newAppSrcWriter(flvSrc *app.Source) *appSrcWriter {
	return &appSrcWriter{
		appSrc: flvSrc,
		eos:    atomic.NewBool(false),
	}
}

func (w *appSrcWriter) Write(p []byte) (int, error) {
	if w.eos.Load() {
		return 0, io.EOF
	}

	b := gst.NewBufferFromBytes(p)

	ret := w.appSrc.PushBuffer(b)
	switch ret {
	case gst.FlowOK, gst.FlowFlushing:
	case gst.FlowEOS:
		w.Close()
		return 0, io.EOF
	default:
		return 0, errors.ErrFromGstFlowReturn(ret)
	}

	return len(p), nil
}

func (w *appSrcWriter) Close() error {
	w.eos.Store(true)

	return nil
}
