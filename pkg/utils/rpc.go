// Copyright 2025 LiveKit, Inc.
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

package utils

import (
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/rpc"
)

func RegisterIngressRpcHandlers(server rpc.IngressHandlerServer, info *livekit.IngressInfo) error {
	if err := server.RegisterUpdateIngressTopic(info.IngressId); err != nil {
		return err
	}
	if err := server.RegisterDeleteIngressTopic(info.IngressId); err != nil {
		return err
	}

	if info.InputType == livekit.IngressInput_WHIP_INPUT {
		if err := server.RegisterDeleteWHIPResourceTopic(info.State.ResourceId); err != nil {
			return err
		}
		if err := server.RegisterICERestartWHIPResourceTopic(info.State.ResourceId); err != nil {
			return err
		}

	}

	return nil
}

func DeregisterIngressRpcHandlers(server rpc.IngressHandlerServer, info *livekit.IngressInfo) {
	server.DeregisterUpdateIngressTopic(info.IngressId)
	server.DeregisterDeleteIngressTopic(info.IngressId)

	if info.InputType == livekit.IngressInput_WHIP_INPUT {
		server.DeregisterDeleteWHIPResourceTopic(info.State.ResourceId)
		server.DeregisterICERestartWHIPResourceTopic(info.State.ResourceId)
	}
}
