package media

import (
	"context"
	"strings"
	"sync"

	"github.com/tinyzimmer/go-gst/gst"
	"github.com/tinyzimmer/go-gst/gst/app"

	"github.com/livekit/ingress/pkg/errors"
	"github.com/livekit/ingress/pkg/media/rtmp"
	"github.com/livekit/ingress/pkg/params"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
)

type StreamKind string

const (
	Audio StreamKind = "audio"
	Video StreamKind = "video"
)

type Source interface {
	GetSource() *app.Source
	Start(ctx context.Context) error
	Close() error
}

type Input struct {
	lock sync.Mutex

	bin    *gst.Bin
	source Source

	audioOutput *gst.Pad
	videoOutput *gst.Pad

	onOutputReady OutputReadyFunc
}

type OutputReadyFunc func(pad *gst.Pad, kind StreamKind)

func NewInput(p *params.Params) (*Input, error) {
	src, err := CreateSource(p)
	if err != nil {
		return nil, err
	}
	appSrc := src.GetSource()

	decodeBin, err := gst.NewElement("decodebin3")
	if err != nil {
		return nil, err
	}

	bin := gst.NewBin("input")
	if err := bin.AddMany(appSrc.Element, decodeBin); err != nil {
		return nil, err
	}

	i := &Input{
		bin:    bin,
		source: src,
	}

	if _, err = decodeBin.Connect("pad-added", i.onPadAdded); err != nil {
		return nil, err
	}

	if err = appSrc.Link(decodeBin); err != nil {
		return nil, err
	}

	return i, nil
}

func CreateSource(p *params.Params) (Source, error) {
	switch p.IngressInfo.InputType {
	case livekit.IngressInput_RTMP_INPUT:
		return rtmp.NewHTTPRelaySource(context.Background(), p)
	case livekit.IngressInput_WHIP_INPUT:
		return nil, nil
	default:
		return nil, errors.ErrInvalidIngressType
	}
}

func (i *Input) OnOutputReady(f OutputReadyFunc) {
	i.onOutputReady = f
}

func (i *Input) Start(ctx context.Context) error {
	return i.source.Start(ctx)
}

func (i *Input) Close() error {
	return i.source.Close()
}

func (i *Input) onPadAdded(_ *gst.Element, pad *gst.Pad) {
	// surface callback for first audio and video pads, plug in fakesink on the rest
	i.lock.Lock()
	newPad := false
	var kind StreamKind
	var ghostPad *gst.GhostPad
	if strings.HasPrefix(pad.GetName(), "audio") {
		if i.audioOutput == nil {
			newPad = true
			kind = Audio
			i.audioOutput = pad
			ghostPad = gst.NewGhostPad("audio", pad)
		}
	} else if strings.HasPrefix(pad.GetName(), "video") {
		if i.videoOutput == nil {
			newPad = true
			kind = Video
			i.videoOutput = pad
			ghostPad = gst.NewGhostPad("video", pad)
		}
	}
	i.lock.Unlock()

	// don't need this pad, link to fakesink
	if newPad {
		if !i.bin.AddPad(ghostPad.Pad) {
			logger.Errorw("failed to add ghost pad", nil)
			return
		}
		pad = ghostPad.Pad
	} else {
		sink, err := gst.NewElement("fakesink")
		if err != nil {
			logger.Errorw("failed to create fakesink", err)
		}
		pads, err := sink.GetSinkPads()
		pad.Link(pads[0])
		return
	}

	if i.onOutputReady != nil {
		i.onOutputReady(pad, kind)
	}
}
