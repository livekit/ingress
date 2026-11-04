# Copyright 2026 LiveKit, Inc.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Build image for Anthropic's OSS Scanner. The scanner passes no build args, so the tag is
# hardcoded: keep it in sync with .gst-version.
FROM livekit/gstreamer:1.28.7-dev

ARG TARGETARCH

ENV DEBIAN_FRONTEND=noninteractive \
    GOTOOLCHAIN=local \
    PATH=/usr/local/go/bin:/root/go/bin:$PATH

RUN apt-get update && apt-get install -y --no-install-recommends \
      build-essential ca-certificates curl git pkg-config redis-server \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /src
COPY . /src

# .go-version is the toolchain the production images use; it must stay >= go.mod's go directive.
RUN ARCH=${TARGETARCH:-$(dpkg --print-architecture)} \
    && curl -fsSL -o /tmp/go.tar.gz "https://go.dev/dl/go$(cat .go-version).linux-${ARCH}.tar.gz" \
    && tar -C /usr/local -xzf /tmp/go.tar.gz \
    && rm /tmp/go.tar.gz \
    && go version

RUN go mod download && go mod verify
RUN go build ./... && go build -o /usr/local/bin/ingress ./cmd/server

# Same subset CI runs without redis or a room server; test/ is the integration suite.
RUN go test -count=1 -timeout 20m ./pkg/... || echo 'WARNING: baseline tests failed'

ENV GOPROXY=off \
    GOSUMDB=off

CMD ["bash"]
