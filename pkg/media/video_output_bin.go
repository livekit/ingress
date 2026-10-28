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

package media

import (
	"github.com/go-gst/go-gst/pkg/gst"

	"github.com/livekit/ingress/pkg/errors"
	"github.com/livekit/ingress/pkg/gstutil"
	"github.com/livekit/protocol/livekit"
)

type VideoOutputBin struct {
	bin                  gst.Bin
	preProcessorElements []gst.Element
	tee                  gst.Element
}

func NewVideoOutputBin(options *livekit.IngressVideoEncodingOptions, outputs []*Output) (*VideoOutputBin, error) {
	o := &VideoOutputBin{}

	o.bin = gstutil.NewBin("video output bin")

	if options.FrameRate > 0 {
		videoRate, err := gstutil.NewElement("videorate")
		if err != nil {
			return nil, err
		}
		videoRate.SetObjectProperty("max-rate", int32(options.FrameRate))
		o.preProcessorElements = append(o.preProcessorElements, videoRate)
	}

	videoConvert, err := gstutil.NewElement("videoconvert")
	if err != nil {
		return nil, err
	}
	o.preProcessorElements = append(o.preProcessorElements, videoConvert)

	err = gstutil.AddMany(o.bin, o.preProcessorElements...)
	if err != nil {
		return nil, err
	}

	err = gstutil.LinkMany(o.preProcessorElements...)
	if err != nil {
		return nil, err
	}

	o.tee, err = gstutil.NewElement("tee")
	if err != nil {
		return nil, err
	}

	err = gstutil.Add(o.bin, o.tee)
	if err != nil {
		return nil, err
	}

	err = gstutil.Link(o.preProcessorElements[len(o.preProcessorElements)-1], o.tee)
	if err != nil {
		return nil, err
	}

	for _, output := range outputs {
		err := gstutil.Add(o.bin, output.bin)
		if err != nil {
			return nil, err
		}

		err = gstutil.Link(o.tee, output.bin)
		if err != nil {
			return nil, err
		}
	}

	if !o.bin.AddPad(gst.NewGhostPad("sink", o.preProcessorElements[0].GetStaticPad("sink"))) {
		return nil, errors.ErrUnableToAddPad
	}

	return o, nil
}

func (o *VideoOutputBin) GetBin() gst.Bin {
	return o.bin
}
