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

package urlpull

import (
	"context"
	"strings"
	"time"

	"github.com/frostbyte73/core"
	"github.com/go-gst/go-gst/gst"

	"github.com/livekit/ingress/pkg/errors"
	"github.com/livekit/ingress/pkg/params"
	"github.com/livekit/protocol/logger"
)

var (
	supportedMimeTypes = []string{
		"audio/x-m4a",
		"application/x-hls",
		"video/quicktime",
		"video/x-matroska",
		"video/webm",
		"video/mpegts",
		"audio/ogg",
		"application/x-id3",
		"audio/mpeg",
	}
)

type URLSource struct {
	params     *params.Params
	src        *gst.Element
	pad        *gst.Pad
	printStats func()

	done core.Fuse
}

func NewURLSource(ctx context.Context, p *params.Params) (*URLSource, error) {
	var printStats func()
	bin := gst.NewBin("input")

	var elem *gst.Element
	var err error
	if strings.HasPrefix(p.Url, "http://") || strings.HasPrefix(p.Url, "https://") {
		elem, err = gst.NewElement("souphttpsrc")
		if err != nil {
			return nil, err
		}

		err = elem.SetProperty("location", p.Url)
		if err != nil {
			return nil, err
		}

	} else if strings.HasPrefix(p.Url, "srt://") {
		elem, err = gst.NewElement("srtclientsrc")
		if err != nil {
			return nil, err
		}
		err = elem.SetProperty("uri", p.Url)
		if err != nil {
			return nil, err
		}

		printStats = func() {
			str, _ := elem.GetProperty("stats")
			if str != nil {
				if v, ok := str.(*gst.Structure); ok {
					logger.Infow("SRT input stats", "stats", v.String())
				}
			}
		}
	} else {
		return nil, errors.ErrUnsupportedURLFormat
	}

	queue, err := gst.NewElement("queue2")
	if err != nil {
		return nil, err
	}

	// Disable buffer count limit and rely on bytes and time limits
	if err := queue.SetProperty("max-size-buffers", uint(0)); err != nil {
		return nil, err
	}

	if strings.HasPrefix(p.Url, "http://") || strings.HasPrefix(p.Url, "https://") {
		err = queue.SetProperty("use-buffering", true)
		if err != nil {
			return nil, err
		}
	}

	err = bin.AddMany(elem, queue)
	if err != nil {
		return nil, err
	}

	err = elem.Link(queue)
	if err != nil {
		return nil, err
	}

	pad := queue.GetStaticPad("src")
	if pad == nil {
		return nil, errors.ErrUnableToAddPad
	}

	ghostPad := gst.NewGhostPad("src", pad)
	if !bin.AddPad(ghostPad.Pad) {
		return nil, errors.ErrUnableToAddPad
	}

	return &URLSource{
		params:     p,
		src:        bin.Element,
		pad:        pad,
		printStats: printStats,
	}, nil
}

func (u *URLSource) GetSources() []*gst.Element {
	return []*gst.Element{
		u.src,
	}
}

func (s *URLSource) ValidateCaps(caps *gst.Caps) error {
	if caps.GetSize() == 0 {
		return errors.ErrUnsupportedDecodeFormat
	}

	str := caps.GetStructureAt(0)
	if str == nil {
		return errors.ErrUnsupportedDecodeFormat
	}

	for _, mime := range supportedMimeTypes {
		if str.Name() == mime {
			return nil
		}
	}

	return errors.ErrUnsupportedDecodeMimeType(str.Name())
}

func (u *URLSource) Start(ctx context.Context, onClose func()) error {
	if u.printStats == nil {
		return nil
	}

	go func() {
		ticker := time.NewTicker(time.Minute)
		for {
			select {
			case <-u.done.Watch():
				ticker.Stop()
				return
			case <-ticker.C:
				u.printStats()
			}
		}
	}()

	return nil
}

func (u *URLSource) Close() error {
	// TODO find a way to send a EOS event without hanging

	u.done.Break()

	return nil
}
