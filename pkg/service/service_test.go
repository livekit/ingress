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

package service

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/livekit/protocol/rpc"
	"github.com/livekit/psrpc"
)

type countingIOInfoClient struct {
	rpc.IOInfoClient
	infoRequests int
}

func (c *countingIOInfoClient) GetIngressInfo(context.Context, *rpc.GetIngressInfoRequest, ...psrpc.RequestOption) (*rpc.GetIngressInfoResponse, error) {
	c.infoRequests++
	return nil, psrpc.NewErrorf(psrpc.Internal, "internal error")
}

func TestPublishRequestWithEmptyStreamKeyIsClientError(t *testing.T) {
	publish := map[string]func(s *Service) error{
		"whip": func(s *Service) error {
			_, _, _, err := s.HandleWHIPPublishRequest("", testResourceID)
			return err
		},
		"rtmp": func(s *Service) error {
			_, _, err := s.HandleRTMPPublishRequest("", testResourceID)
			return err
		},
	}

	for name, fn := range publish {
		t.Run(name, func(t *testing.T) {
			client := &countingIOInfoClient{}
			svc := &Service{psrpcClient: client}

			err := fn(svc)

			var psrpcErr psrpc.Error
			require.ErrorAs(t, err, &psrpcErr)
			require.Equal(t, http.StatusBadRequest, psrpcErr.ToHttp(), "got %v", err)
			require.Zero(t, client.infoRequests, "an empty stream key must not be looked up")
		})
	}
}
