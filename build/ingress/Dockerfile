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
ARG GOVERSION

WORKDIR /workspace

# install go
RUN apt-get update && apt-get install -y curl

RUN if [ "$TARGETPLATFORM" = "linux/arm64" ]; then GOARCH=arm64; else GOARCH=amd64; fi && \
    curl -L -o /tmp/go.tar.gz "https://go.dev/dl/go$GOVERSION.linux-$GOARCH.tar.gz"
RUN tar -C /usr/local -xzf /tmp/go.tar.gz
ENV PATH="$PATH:/usr/local/go/bin"

# download go modules
COPY go.mod .
COPY go.sum .
RUN go mod download

# copy source
COPY cmd/ cmd/
COPY pkg/ pkg/
COPY version/ version/

# build
RUN if [ "$TARGETPLATFORM" = "linux/arm64" ]; then GOARCH=arm64; else GOARCH=amd64; fi && \
    CGO_ENABLED=1 GOOS=linux GOARCH=${GOARCH} GO111MODULE=on go build -a -o ingress ./cmd/server

ARG GSTVERSION
FROM livekit/gstreamer:$GSTVERSION-prod

# install wget for health check
RUN apt-get update && apt-get install -y wget

# clean up
RUN apt-get clean && \
    rm -rf /var/lib/apt/lists/* 

# copy binary
COPY --from=0 /workspace/ingress /bin/

# run
ENTRYPOINT ["ingress"]
