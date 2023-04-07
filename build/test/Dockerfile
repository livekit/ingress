FROM livekit/gstreamer:1.20.4-dev

ARG TARGETPLATFORM

WORKDIR /workspace

# install deps
RUN apt-get update && \
    apt-get install -y \
    golang 

# download go modules
COPY go.mod .
COPY go.sum .
RUN go mod download

# copy source
COPY pkg/ pkg/
COPY version/ version/

COPY build/test/entrypoint.sh .
ENTRYPOINT ["./entrypoint.sh"]
