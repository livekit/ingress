package media

import (
	"errors"
	"io"
	"strings"
	"sync"

	"github.com/tinyzimmer/go-gst/gst"
	"github.com/tinyzimmer/go-gst/gst/app"
)

// Input creates gstreamer elements that converts input bytes into
// decoded audio/video outputs.
type Input struct {
	source *app.Source
	lock   sync.Mutex

	// decodebin decodes the stream
	decodeBin *gst.Element

	audioOutput *gst.Pad
	videoOutput *gst.Pad

	onOutputReady func(input *Input, pad *gst.Pad, kind StreamKind)
}

func NewInput() (*Input, error) {
	i := &Input{}
	var err error
	if i.source, err = app.NewAppSrc(); err != nil {
		return nil, err
	}

	if i.decodeBin, err = gst.NewElement("decodebin3"); err != nil {
		return nil, err
	}

	if _, err = i.decodeBin.Connect("pad-added", i.onPadAdded); err != nil {
		return nil, err
	}
	return i, nil
}

func (i *Input) Write(b []byte) error {
	r := i.source.PushBuffer(gst.NewBufferFromBytes(b))
	switch r {
	case gst.FlowOK, gst.FlowFlushing:
		// TODO: unsure if flushing should return error
		return nil
	case gst.FlowEOS:
		return io.EOF
	default:
		return errors.New(r.String())
	}
}

func (i *Input) onPadAdded(element *gst.Element, pad *gst.Pad) {
	// surface callback for first audio and video pads, plug in fakesink on the rest
	i.lock.Lock()
	newPad := false
	var kind StreamKind
	if strings.HasPrefix(pad.GetName(), "audio") {
		if i.audioOutput == nil {
			newPad = true
			kind = Audio
			i.audioOutput = pad
		}
	} else if strings.HasPrefix(pad.GetName(), "video") {
		if i.videoOutput == nil {
			newPad = true
			kind = Video
			i.videoOutput = pad
		}
	}
	i.lock.Unlock()

	// don't need this pad, link to fakesink
	if !newPad {
		sink, err := gst.NewElement("fakesink")
		if err != nil {
			// TODO: log
		}
		pads, err := sink.GetSinkPads()
		pad.Link(pads[0])
		return
	}

	if i.onOutputReady != nil {
		i.onOutputReady(i, pad, kind)
	}
}
