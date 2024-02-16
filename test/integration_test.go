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

package test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/livekit/ingress/pkg/service"
	"github.com/livekit/protocol/redis"
	"github.com/livekit/psrpc"
)

func TestIngress(t *testing.T) {
	conf := getConfig(t)

	rc, err := redis.GetRedisClient(conf.Redis)
	require.NoError(t, err)
	require.NotNil(t, rc, "redis required")

	bus := psrpc.NewRedisMessageBus(rc)

	RunTestSuite(t, conf, bus, service.NewCmd)
}
