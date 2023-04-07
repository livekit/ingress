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
COPY pkg/ pkg/

# build tests
RUN if [ "$TARGETPLATFORM" = "linux/arm64" ]; then GOARCH=arm64; else GOARCH=amd64; fi && \
    CGO_ENABLED=1 GOOS=linux GOARCH=${GOARCH} GO111MODULE=on go test -c -v ./pkg/...

FROM livekit/gstreamer:1.20.4-prod

ARG TARGETPLATFORM

# install deps
RUN apt-get update && \
    apt-get install -y \
    golang 


# copy build
COPY --from=0 /workspace/test.test .

COPY build/test/entrypoint.sh .
ENTRYPOINT ["./entrypoint.sh"]
