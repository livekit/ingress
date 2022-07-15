package media

import (
	"strings"
	"sync"

	"github.com/tinyzimmer/go-gst/gst"

	"github.com/livekit/protocol/logger"
)

type Input struct {
	lock sync.Mutex

	bin *gst.Bin
	// source *app.Source TODO

	audioOutput *gst.Pad
	videoOutput *gst.Pad

	onOutputReady OutputReadyFunc
}

type OutputReadyFunc func(pad *gst.Pad, kind StreamKind)

func NewInput(p *Params) (*Input, error) {
	rtmp2src, err := gst.NewElement("rtmp2src")
	if err != nil {
		return nil, err
	}
	if err = rtmp2src.SetProperty("location", p.Url); err != nil {
		return nil, err
	}

	// queue, err := gst.NewElement("queue")
	// if err != nil {
	// 	return nil, err
	// }

	decodeBin, err := gst.NewElement("decodebin3")
	if err != nil {
		return nil, err
	}

	bin := gst.NewBin("input")
	if err := bin.AddMany(rtmp2src, decodeBin); err != nil {
		return nil, err
	}

	i := &Input{bin: bin}

	if _, err = decodeBin.Connect("pad-added", i.onPadAdded); err != nil {
		return nil, err
	}

	if err = rtmp2src.Link(decodeBin); err != nil {
		return nil, err
	}

	return i, nil
}

// TODO: replace rtmp2src with appsrc
// func (i *Input) Write(b []byte) error {
// 	r := i.source.PushBuffer(gst.NewBufferFromBytes(b))
// 	switch r {
// 	case gst.FlowOK, gst.FlowFlushing:
// 		// TODO: unsure if flushing should return error
// 		return nil
// 	case gst.FlowEOS:
// 		return io.EOF
// 	default:
// 		return errors.New(r.String())
// 	}
// }

func (i *Input) OnOutputReady(f OutputReadyFunc) {
	i.onOutputReady = f
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
