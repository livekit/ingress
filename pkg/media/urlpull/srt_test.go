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

package urlpull

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/livekit/protocol/livekit"

	"github.com/livekit/ingress/pkg/config"
	ierrors "github.com/livekit/ingress/pkg/errors"
	"github.com/livekit/ingress/pkg/params"
)

const publicSRTURL = "srt://93.184.215.14:9000"

func stubSRTLookup(t *testing.T) {
	orig := lookupNetIP
	t.Cleanup(func() { lookupNetIP = orig })
	lookupNetIP = func(_ context.Context, _, host string) ([]netip.Addr, error) {
		switch strings.ToLower(host) {
		case "public.example", "localhost", "foo.localhost":
			return []netip.Addr{netip.MustParseAddr("93.184.215.14")}, nil
		case "v6.example":
			return []netip.Addr{netip.MustParseAddr("2606:2800:21f:cb07:6820:80da:af6b:8b2c")}, nil
		case "mapped.example":
			return []netip.Addr{netip.MustParseAddr("::ffff:93.184.215.14")}, nil
		case "internal.example":
			return []netip.Addr{netip.MustParseAddr("10.1.2.3")}, nil
		case "mixed.example":
			return []netip.Addr{netip.MustParseAddr("93.184.215.14"), netip.MustParseAddr("169.254.169.254")}, nil
		}
		if addr, err := netip.ParseAddr(host); err == nil {
			return []netip.Addr{addr}, nil
		}
		return nil, errors.New("no such host")
	}
}

func TestPinSRTURLAccepted(t *testing.T) {
	stubSRTLookup(t)
	for in, want := range map[string]string{
		"srt://public.example:9000":                        publicSRTURL,
		publicSRTURL:                                       publicSRTURL,
		"srt://public.example":                             "srt://93.184.215.14",
		"srt://v6.example:9000":                            "srt://[2606:2800:21f:cb07:6820:80da:af6b:8b2c]:9000",
		"srt://v6.example":                                 "srt://[2606:2800:21f:cb07:6820:80da:af6b:8b2c]",
		"srt://mapped.example:9000":                        publicSRTURL,
		"srt://[2606:2800:21f:cb07:6820:80da:af6b:8b2c]:1": "srt://[2606:2800:21f:cb07:6820:80da:af6b:8b2c]:1",
		"srt://public.example:9000?mode=caller&streamid=a%20b+c%26d%3De&latency=200":                                                               "srt://93.184.215.14:9000?mode=caller&streamid=a%20b%2Bc%26d%3De&latency=200",
		"srt://public.example:9000?streamid=#!::r=live,m=request":                                                                                  "srt://93.184.215.14:9000?streamid=",
		"srt://user@public.example:9000/path?passphrase=0123456789&pbkeylen=16&rcvlatency=1&peerlatency=2&enforcedencryption=true&conntimeo=3000&": "srt://93.184.215.14:9000?passphrase=0123456789&pbkeylen=16&rcvlatency=1&peerlatency=2&enforcedencryption=true&conntimeo=3000",
	} {
		t.Run(in, func(t *testing.T) {
			got, err := pinSRTURL(context.Background(), in)
			require.NoError(t, err)
			require.Equal(t, want, got)
		})
	}
}

func TestPinSRTURLRejected(t *testing.T) {
	stubSRTLookup(t)
	for in, want := range map[string]error{
		"srt://127.0.0.1:9000":                                ierrors.ErrSRTHostNotAllowed,
		"srt://[::1]:9000":                                    ierrors.ErrSRTHostNotAllowed,
		"srt://169.254.169.254:9000":                          ierrors.ErrSRTHostNotAllowed,
		"srt://[::ffff:169.254.169.254]:9000":                 ierrors.ErrSRTHostNotAllowed,
		"srt://[2606:2800:21f:cb07::1%25eth0]:9000":           ierrors.ErrSRTHostNotAllowed,
		"srt://localhost:9000":                                ierrors.ErrSRTHostNotAllowed,
		"srt://foo.localhost:9000":                            ierrors.ErrSRTHostNotAllowed,
		"srt://internal.example:9000":                         ierrors.ErrSRTHostNotAllowed,
		"srt://mixed.example:9000":                            ierrors.ErrSRTHostNotAllowed,
		"srt://unresolvable.example:9000":                     ierrors.ErrSRTHostUnresolved,
		"srt://:9000":                                         ierrors.ErrInvalidSRTURL,
		"srt://:9000?mode=listener":                           ierrors.ErrInvalidSRTURL,
		"http://public.example:9000":                          ierrors.ErrInvalidSRTURL,
		"srt://public.example:9000?mode=listener":             ierrors.ErrInvalidSRTURL,
		"srt://public.example:9000?mode=rendezvous":           ierrors.ErrInvalidSRTURL,
		"srt://public.example:9000?mod%65=listener":           ierrors.ErrInvalidSRTURL,
		"srt://public.example:9000?MODE=listener":             ierrors.ErrInvalidSRTURL,
		"srt://public.example:9000?localaddress=0.0.0.0":      ierrors.ErrInvalidSRTURL,
		"srt://public.example:9000?localport=22":              ierrors.ErrInvalidSRTURL,
		"srt://public.example:9000?bindtodevice=eth0":         ierrors.ErrInvalidSRTURL,
		"srt://public.example:9000?poll-timeout=1":            ierrors.ErrInvalidSRTURL,
		"srt://public.example:9000?streamid=a&streamid=b":     ierrors.ErrInvalidSRTURL,
		"srt://public.example:9000?mode=caller&mode=listener": ierrors.ErrInvalidSRTURL,
		"srt://public.example:9000?streamid":                  ierrors.ErrInvalidSRTURL,
		"srt://public.example:9000?streamid=%zz":              ierrors.ErrInvalidSRTURL,
		"srt://public.example:9000?streamid=a%00b":            ierrors.ErrInvalidSRTURL,
	} {
		t.Run(in, func(t *testing.T) {
			_, err := pinSRTURL(context.Background(), in)
			require.ErrorIs(t, err, want)
		})
	}
}

func TestPinSRTURLLookupHasDeadline(t *testing.T) {
	orig := lookupNetIP
	t.Cleanup(func() { lookupNetIP = orig })
	lookupNetIP = func(ctx context.Context, _, _ string) ([]netip.Addr, error) {
		deadline, ok := ctx.Deadline()
		require.True(t, ok, "lookup context has no deadline")
		require.WithinDuration(t, time.Now().Add(srtLookupTimeout), deadline, time.Second)
		return []netip.Addr{netip.MustParseAddr("93.184.215.14")}, nil
	}
	_, err := pinSRTURL(context.Background(), "srt://public.example:9000")
	require.NoError(t, err)
}

func newSRTPullParams(inputURL string, block bool) *params.Params {
	return &params.Params{
		IngressInfo: &livekit.IngressInfo{InputType: livekit.IngressInput_URL_INPUT, Url: inputURL},
		Config:      &config.Config{ServiceConfig: &config.ServiceConfig{BlockInternalSRTPull: block}},
	}
}

func srtElementURI(t *testing.T, src *URLSource) string {
	queue := src.pad.GetParentElement()
	require.NotNil(t, queue)
	elem := queue.GetStaticPad("sink").GetPeer().GetParentElement()
	require.NotNil(t, elem)
	uri, ok := elem.ObjectProperty("uri").(string)
	require.True(t, ok)
	return uri
}

func TestNewURLSourceSRTHost(t *testing.T) {
	stubSRTLookup(t)

	t.Run("srtclientsrc gets the checked address", func(t *testing.T) {
		src, err := NewURLSource(context.Background(), newSRTPullParams("srt://public.example:9000?streamid=s", true))
		require.NoError(t, err)
		t.Cleanup(func() { _ = src.Close() })
		require.Equal(t, "srt://93.184.215.14:9000?streamid=s", srtElementURI(t, src))
	})

	t.Run("internal host refused", func(t *testing.T) {
		_, err := NewURLSource(context.Background(), newSRTPullParams("srt://internal.example:9000", true))
		require.ErrorIs(t, err, ierrors.ErrSRTHostNotAllowed)
		_, err = NewURLSource(context.Background(), newSRTPullParams("srt://public.example:9000?mode=listener", true))
		require.ErrorIs(t, err, ierrors.ErrInvalidSRTURL)
	})

	t.Run("url passed through when not enabled", func(t *testing.T) {
		src, err := NewURLSource(context.Background(), newSRTPullParams("srt://internal.example:9000?mode=listener", false))
		require.NoError(t, err)
		t.Cleanup(func() { _ = src.Close() })
		require.Equal(t, "srt://internal.example:9000?mode=listener", srtElementURI(t, src))
	})
}
