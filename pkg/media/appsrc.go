package media

import (
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/livekit/ingress/pkg/errors"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/tracer"
	"github.com/tinyzimmer/go-gst/gst"
	"github.com/tinyzimmer/go-gst/gst/app"
	"go.uber.org/atomic"
)

const (
	FlvAppSource = "flvAppSrc"
)

type HTTPRelaySource struct {
	logger logger.Logger
	params *Params

	flvSrc *app.Source
	writer *appSrcWriter
}

func NewHTTPRelaySource(ctx context.Context, p *Params) (*HTTPRelaySource, error) {
	ctx, span := tracer.Start(ctx, "HTTPRelaySource.New")
	defer span.End()

	s := &HTTPRelaySource{
		logger: p.Logger,
		params: p,
	}

	src, err := gst.NewElementWithName("appsrc", FlvAppSource)
	if err != nil {
		s.logger.Errorw("could not create appsrc", err)
		return nil, err
	}
	src.SetProperty("caps", "video/x-flv")
	src.SetArg("format", "time")
	if err := src.SetProperty("is-live", true); err != nil {
		return nil, err
	}
	s.flvSrc = app.SrcFromElement(src)
	s.writer = newAppSrcWriter(s.flvSrc)

	return s, nil
}

func (s *HTTPRelaySource) Start(ctx context.Context) error {
	resp, err := http.Get(s.params.RelayUrl)
	switch {
	case err != nil:
		return err
	case resp != nil && (resp.StatusCode < 200 || resp.StatusCode >= 400):
		return fmt.Errorf("HTTP request failed with code %d", resp.StatusCode)
	}

	go func() {
		defer resp.Body.Close()

		_, err := io.Copy(s.writer, resp.Body)
		switch err {
		case nil, io.EOF:
		default:
			s.logger.Errorw("error while copying media from relay", err)
		}
	}()

	return nil
}

func (s *HTTPRelaySource) Close() error {
	return s.writer.Close()
}

func (s *HTTPRelaySource) GetSource() *app.Source {
	return s.flvSrc
}

type appSrcWriter struct {
	flvSrc *app.Source
	eos    *atomic.Bool
}

func newAppSrcWriter(flvSrc *app.Source) *appSrcWriter {
	return &appSrcWriter{
		flvSrc: flvSrc,
		eos:    atomic.NewBool(false),
	}
}

func (w *appSrcWriter) Write(p []byte) (int, error) {
	if w.eos.Load() {
		return 0, io.EOF
	}

	b := gst.NewBufferFromBytes(p)

	ret := w.flvSrc.PushBuffer(b)
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
