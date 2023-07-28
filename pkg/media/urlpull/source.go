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

	"github.com/tinyzimmer/go-gst/gst"
	"github.com/tinyzimmer/go-gst/gst/base"

	"github.com/livekit/ingress/pkg/params"
)

type URLSource struct {
	params      *params.Params
	curlHttpSrc *base.GstBaseSrc
}

func NewURLSource(ctx context.Context, p *params.Params) (*URLSource, error) {
	elem, err := gst.NewElement("curlhttpsrc")
	if err != nil {
		return nil, err
	}

	elem.SetProperty("location", p.Url)

	return &URLSource{
		params:      p,
		curlHttpSrc: &base.GstBaseSrc{Element: elem},
	}, nil
}

func (u *URLSource) GetSources(ctx context.Context) []*base.GstBaseSrc {
	return []*base.GstBaseSrc{
		u.curlHttpSrc,
	}
}

func (u *URLSource) Start(ctx context.Context) error {
	return nil
}

func (u *URLSource) Close() error {
	return nil
}
