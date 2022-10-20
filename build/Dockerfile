FROM livekit/gstreamer:1.20.4-dev

ARG TARGETPLATFORM

WORKDIR /workspace

# install go
RUN apt-get update && apt-get install -y golang

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

FROM livekit/gstreamer:1.20.4-prod

# clean up
RUN apt-get clean && \
    rm -rf /var/lib/apt/lists/* 

# copy binary
COPY --from=0 /workspace/ingress /bin/

# run
ENTRYPOINT ["ingress"]
