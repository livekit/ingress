// Copyright 2024 LiveKit, Inc.
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

package types

import (
	"context"
)

type SessionAPI interface {
	MediaStatsUpdater
	MediaStatGatherer

	GetProfileData(ctx context.Context, profileName string, timeout int, debug int) (b []byte, err error)
	GetPipelineDot(ctx context.Context) (string, error)
	CloseSession(ctx context.Context)
}
