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
	"github.com/go-gst/go-gst/pkg/gst"

	"github.com/livekit/protocol/logger"

	"github.com/livekit/ingress/pkg/errors"
	"github.com/livekit/ingress/pkg/gstutil"
	"github.com/livekit/ingress/pkg/params"
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
	src        gst.Element
	pad        gst.Pad
	printStats func()

	done core.Fuse
}

func NewURLSource(_ context.Context, p *params.Params) (*URLSource, error) {
	var printStats func()
	bin := gstutil.NewBin("input")

	var elem gst.Element
	var err error
	if strings.HasPrefix(p.Url, "http://") || strings.HasPrefix(p.Url, "https://") {
		elem, err = gstutil.NewElement("souphttpsrc")
		if err != nil {
			return nil, err
		}

		elem.SetObjectProperty("location", p.Url)

	} else if strings.HasPrefix(p.Url, "srt://") {
		elem, err = gstutil.NewElement("srtclientsrc")
		if err != nil {
			return nil, err
		}
		elem.SetObjectProperty("uri", p.Url)

		printStats = func() {
			if v, ok := elem.ObjectProperty("stats").(*gst.Structure); ok && v != nil {
				logger.Infow("SRT input stats", "stats", v.String())
			}
		}
	} else if p.EnableUDPURLPull && strings.HasPrefix(p.Url, "udp://") {
		elem, err = gstutil.NewElement("udpsrc")
		if err != nil {
			return nil, err
		}
		elem.SetObjectProperty("uri", p.Url)

		if p.MulticastInterface != "" {
			elem.SetObjectProperty("multicast-iface", p.MulticastInterface)
		}

		// udpsrc doesn't expose a stats property, so leave printStats unset
	} else {
		return nil, errors.ErrUnsupportedURLFormat
	}

	queue, err := gstutil.NewElement("queue2")
	if err != nil {
		return nil, err
	}

	// Disable buffer count limit and rely on bytes and time limits
	queue.SetObjectProperty("max-size-buffers", uint32(0))

	if strings.HasPrefix(p.Url, "http://") || strings.HasPrefix(p.Url, "https://") {
		queue.SetObjectProperty("use-buffering", true)
	}

	err = gstutil.AddMany(bin, elem, queue)
	if err != nil {
		return nil, err
	}

	err = gstutil.Link(elem, queue)
	if err != nil {
		return nil, err
	}

	pad := queue.GetStaticPad("src")
	if pad == nil {
		return nil, errors.ErrUnableToAddPad
	}

	if !bin.AddPad(gst.NewGhostPad("src", pad)) {
		return nil, errors.ErrUnableToAddPad
	}

	return &URLSource{
		params:     p,
		src:        bin,
		pad:        pad,
		printStats: printStats,
	}, nil
}

func (u *URLSource) GetSources() []gst.Element {
	return []gst.Element{
		u.src,
	}
}

func (u *URLSource) ValidateCaps(caps *gst.Caps) error {
	if caps.GetSize() == 0 {
		return errors.ErrUnsupportedDecodeFormat
	}

	str := caps.GetStructure(0)
	if str == nil {
		return errors.ErrUnsupportedDecodeFormat
	}

	for _, mime := range supportedMimeTypes {
		if str.GetName() == mime {
			return nil
		}
	}

	return errors.ErrUnsupportedDecodeMimeType(str.GetName())
}

func (u *URLSource) Start(_ context.Context, _ func()) error {
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
