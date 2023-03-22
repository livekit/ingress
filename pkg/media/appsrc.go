package media

import (
	"context"
	"io"
	"net/http"

	"github.com/tinyzimmer/go-gst/gst"
	"github.com/tinyzimmer/go-gst/gst/app"
	"go.uber.org/atomic"

	"github.com/livekit/ingress/pkg/errors"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/tracer"
)

const (
	FlvAppSource = "flvAppSrc"
)

type HTTPRelaySource struct {
	params *Params

	flvSrc *app.Source
	writer *appSrcWriter
	result chan error
}

func NewHTTPRelaySource(ctx context.Context, p *Params) (*HTTPRelaySource, error) {
	ctx, span := tracer.Start(ctx, "HTTPRelaySource.New")
	defer span.End()

	s := &HTTPRelaySource{
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

func (s *HTTPRelaySource) Start(ctx context.Context) error {
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

func (s *HTTPRelaySource) Close() error {
	s.writer.Close()
	return <-s.result
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
