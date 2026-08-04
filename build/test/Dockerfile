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

ARG LINT_VERSION=v2.11.3

FROM livekit/gstreamer:1.26.7-dev AS builder

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

ENV GOMODCACHE=/go/pkg/mod \
    GOCACHE=/go/build-cache

# download go modules
COPY go.mod .
COPY go.sum .
RUN --mount=type=cache,target=/go/pkg/mod,sharing=locked \
    --mount=type=cache,target=/go/build-cache \
    go mod download

# Get security updates in case the toolchain was updated.
# SECURITY_REFRESH busts the cache so the upgrade re-runs on every build.
ARG SECURITY_REFRESH
RUN apt-get update && apt-get -y dist-upgrade && apt-get install -y curl && rm -rf /var/lib/apt/lists/*

# copy source
COPY pkg/ pkg/
COPY version/ version/

FROM golangci/golangci-lint:${LINT_VERSION} AS golangci

FROM builder AS lint

COPY --from=golangci /usr/bin/golangci-lint /usr/local/bin/golangci-lint
COPY .golangci.yaml .

RUN --mount=type=cache,target=/go/pkg/mod,sharing=locked \
    --mount=type=cache,target=/go/build-cache \
    CGO_ENABLED=1 \
    golangci-lint run \
        --timeout=5m \
        --modules-download-mode=mod
RUN echo ok >/lint_ok

FROM builder

# Force lint stage to run successfully
COPY --from=lint /lint_ok /__lint_ok

COPY build/test/entrypoint.sh .
ENTRYPOINT ["./entrypoint.sh"]
