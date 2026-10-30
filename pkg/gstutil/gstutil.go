// Copyright 2026 LiveKit, Inc.
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

// Package gstutil turns the bool and nil results of the go-gst bindings into errors.
package gstutil

import (
	"fmt"

	"github.com/go-gst/go-glib/pkg/glib/v2"
	"github.com/go-gst/go-gst/pkg/gst"
	"github.com/livekit/psrpc"
)

func NewElement(factory string) (gst.Element, error) {
	return NewElementWithName(factory, "")
}

func NewElementWithName(factory, name string) (gst.Element, error) {
	e := gst.ElementFactoryMake(factory, name)
	if e == nil {
		return nil, psrpc.NewErrorf(psrpc.Internal, "could not create element %s", factory)
	}
	return e, nil
}

func NewBin(name string) gst.Bin {
	return gst.NewBin(name).(gst.Bin)
}

func NewPipeline(name string) gst.Pipeline {
	return gst.NewPipeline(name).(gst.Pipeline)
}

// ParseLaunch builds a pipeline from a gst-launch description.
func ParseLaunch(description string) (gst.Pipeline, error) {
	e, err := gst.ParseLaunch(description)
	if err != nil {
		return nil, err
	}
	p, ok := e.(gst.Pipeline)
	if !ok {
		return nil, fmt.Errorf("%q is not a pipeline", description)
	}
	return p, nil
}

func Add(bin gst.Bin, element gst.Element) error {
	if !bin.Add(element) {
		return psrpc.NewErrorf(psrpc.Internal, "could not add %s to %s", element.GetName(), bin.GetName())
	}
	return nil
}

func AddMany(bin gst.Bin, elements ...gst.Element) error {
	for _, e := range elements {
		if err := Add(bin, e); err != nil {
			return err
		}
	}
	return nil
}

func Link(src, dst gst.Element) error {
	if !src.Link(dst) {
		return psrpc.NewErrorf(psrpc.Internal, "could not link %s to %s", src.GetName(), dst.GetName())
	}
	return nil
}

func LinkMany(elements ...gst.Element) error {
	for i := 1; i < len(elements); i++ {
		if err := Link(elements[i-1], elements[i]); err != nil {
			return err
		}
	}
	return nil
}

func SinkPads(e gst.Element) []gst.Pad {
	var pads []gst.Pad
	for v := range e.IterateSinkPads().Values() {
		if pad, ok := v.(gst.Pad); ok {
			pads = append(pads, pad)
		}
	}
	return pads
}

// Caps returns the caps held in an element's "caps" property, or nil when none are set.
func Caps(e gst.Element) *gst.Caps {
	caps, _ := e.ObjectProperty("caps").(*gst.Caps)
	return caps
}

// NewBufferFromBytes returns a buffer holding a copy of data.
func NewBufferFromBytes(data []byte) *gst.Buffer {
	return gst.NewBufferWrappedBytes(glib.NewBytes(data))
}

// SetStateBlocking changes the element's state and waits for an asynchronous change to finish.
func SetStateBlocking(e gst.Element, state gst.State) error {
	if ret := e.BlockSetState(state, gst.ClockTimeNone); ret == gst.StateChangeFailure {
		return psrpc.NewErrorf(psrpc.Internal, "could not set %s to %s", e.GetName(), state)
	}
	return nil
}

// SourceName returns the name of the object that posted msg, or "" when it has none.
func SourceName(msg *gst.Message) string {
	if src := msg.Source(); src != nil {
		return src.GetName()
	}
	return ""
}
