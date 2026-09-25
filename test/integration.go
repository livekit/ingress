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

//go:build integration

package test

import (
	"context"
	"os/exec"
	"testing"

	"github.com/livekit/protocol/rpc"
	"github.com/livekit/psrpc"

	"github.com/livekit/ingress/pkg/params"
	"github.com/livekit/ingress/pkg/utils"
)

func RunTestSuite(
	t *testing.T,
	r *Runner,
	bus psrpc.MessageBus,
	getStateNotifier func(psrpcClient rpc.IOInfoClient) utils.StateNotifier,
	newCmd func(ctx context.Context, p *params.Params) (*exec.Cmd, error),
) {
	r.StartServer(t, bus, getStateNotifier, newCmd)

	r.testRTMP(t)
	r.testURL(t)
	r.testWHIP(t)
}
