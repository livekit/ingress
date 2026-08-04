# Copyright 2023 LiveKit, Inc.
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

ARG GSTVERSION=1.26.7

FROM livekit/gstreamer:$GSTVERSION-dev

ARG TARGETPLATFORM

WORKDIR /workspace

COPY .go-version ./

# Install Go: use the exact patch pinned in .go-version, which Renovate keeps current
# via an approvable PR. It is decoupled from go.mod's "go" directive (the module
# minimum, which the go toolchain may rewrite to bare major.minor). GOTOOLCHAIN=local
# forces exactly this toolchain and fails the build if go.mod ever requires a newer
# one, so a minor-version bump surfaces as a red CI check instead of a silent download.
RUN if [ "$TARGETPLATFORM" = "linux/arm64" ]; then GOARCH=arm64; else GOARCH=amd64; fi && \
    GOVER=$(cat .go-version) && \
    if [ -z "$GOVER" ]; then echo ".go-version is empty" >&2; exit 1; fi && \
    echo "using Go $GOVER from .go-version" && \
    curl -L -o /tmp/go.tar.gz "https://go.dev/dl/go$GOVER.linux-$GOARCH.tar.gz"
RUN tar -C /usr/local -xzf /tmp/go.tar.gz
ENV PATH="$PATH:/usr/local/go/bin"
ENV GOTOOLCHAIN=local

# download go modules
COPY go.mod go.sum ./
RUN go mod download

# Get security updates in case the toolchain was updated.
# SECURITY_REFRESH busts the cache so the upgrade re-runs on every build.
ARG SECURITY_REFRESH
RUN apt-get update && apt-get -y dist-upgrade && apt-get install -y curl && rm -rf /var/lib/apt/lists/*

# copy source
COPY cmd/ cmd/
COPY pkg/ pkg/
COPY version/ version/

# build
RUN if [ "$TARGETPLATFORM" = "linux/arm64" ]; then GOARCH=arm64; else GOARCH=amd64; fi && \
    CGO_ENABLED=1 GOOS=linux GOARCH=${GOARCH} GO111MODULE=on go build -a -o ingress ./cmd/server

ARG GSTVERSION
FROM livekit/gstreamer:$GSTVERSION-prod

# install wget for health check, pulling in security updates. SECURITY_REFRESH
# busts the cache so the upgrade re-runs on every build.
ARG SECURITY_REFRESH
RUN apt-get update && apt-get -y dist-upgrade && apt-get install -y wget && rm -rf /var/lib/apt/lists/*

# copy binary
COPY --from=0 /workspace/ingress /bin/

# run
ENTRYPOINT ["ingress"]
