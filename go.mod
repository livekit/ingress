module github.com/livekit/ingress

replace github.com/livekit/server-sdk-go/v2 => ../server-sdk-go

go 1.21

toolchain go1.21.4

require (
	github.com/Eyevinn/mp4ff v0.42.0
	github.com/aclements/go-moremath v0.0.0-20210112150236-f10218a38794
	github.com/frostbyte73/core v0.0.10
	github.com/go-gst/go-glib v0.0.0-20231207075824-6d6aaf082c65
	github.com/go-gst/go-gst v0.0.0-20240207190302-04ec17f96d71
	github.com/gorilla/mux v1.8.1
	github.com/livekit/go-rtmp v0.0.0-20230829211117-1c4f5a5c81ed
	github.com/livekit/mageutil v0.0.0-20230125210925-54e8a70427c1
	github.com/livekit/mediatransportutil v0.0.0-20240302142739-1c3dd691a1b8
	github.com/livekit/protocol v1.11.0
	github.com/livekit/psrpc v0.5.3-0.20240315045730-ba2e5b9923b5
	github.com/livekit/server-sdk-go/v2 v2.0.7-0.20240315195514-46474eb45977
	github.com/pion/dtls/v2 v2.2.10
	github.com/pion/interceptor v0.1.25
	github.com/pion/rtcp v1.2.14
	github.com/pion/rtp v1.8.3
	github.com/pion/sdp/v3 v3.0.6
	github.com/pion/webrtc/v3 v3.2.28
	github.com/prometheus/client_golang v1.19.0
	github.com/sirupsen/logrus v1.9.3
	github.com/stretchr/testify v1.9.0
	github.com/urfave/cli/v2 v2.27.1
	github.com/yutopp/go-flv v0.3.1
	go.uber.org/atomic v1.11.0
	golang.org/x/image v0.15.0
	google.golang.org/grpc v1.62.0
	google.golang.org/protobuf v1.33.0
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/bep/debounce v1.2.1 // indirect
	github.com/cespare/xxhash/v2 v2.2.0 // indirect
	github.com/cpuguy83/go-md2man/v2 v2.0.2 // indirect
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/dgryski/go-rendezvous v0.0.0-20200823014737-9f7001d12a5f // indirect
	github.com/eapache/channels v1.1.0 // indirect
	github.com/eapache/queue v1.1.0 // indirect
	github.com/gammazero/deque v0.2.1 // indirect
	github.com/go-jose/go-jose/v3 v3.0.3 // indirect
	github.com/go-logr/logr v1.4.1 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/golang/protobuf v1.5.3 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/gorilla/websocket v1.5.1 // indirect
	github.com/hashicorp/errwrap v1.1.0 // indirect
	github.com/hashicorp/go-multierror v1.1.0 // indirect
	github.com/jxskiss/base62 v1.1.0 // indirect
	github.com/klauspost/compress v1.17.6 // indirect
	github.com/klauspost/cpuid/v2 v2.2.6 // indirect
	github.com/lithammer/shortuuid/v4 v4.0.0 // indirect
	github.com/mackerelio/go-osstat v0.2.4 // indirect
	github.com/magefile/mage v1.15.0 // indirect
	github.com/mattn/go-pointer v0.0.1 // indirect
	github.com/mitchellh/mapstructure v1.4.1 // indirect
	github.com/nats-io/nats.go v1.31.0 // indirect
	github.com/nats-io/nkeys v0.4.7 // indirect
	github.com/nats-io/nuid v1.0.1 // indirect
	github.com/pion/datachannel v1.5.5 // indirect
	github.com/pion/ice/v2 v2.3.13 // indirect
	github.com/pion/logging v0.2.2 // indirect
	github.com/pion/mdns v0.0.12 // indirect
	github.com/pion/randutil v0.1.0 // indirect
	github.com/pion/sctp v1.8.12 // indirect
	github.com/pion/srtp/v2 v2.0.18 // indirect
	github.com/pion/stun v0.6.1 // indirect
	github.com/pion/transport/v2 v2.2.4 // indirect
	github.com/pion/turn/v2 v2.1.4 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/prometheus/client_model v0.5.0 // indirect
	github.com/prometheus/common v0.48.0 // indirect
	github.com/prometheus/procfs v0.12.0 // indirect
	github.com/puzpuzpuz/xsync v1.5.2 // indirect
	github.com/redis/go-redis/v9 v9.5.1 // indirect
	github.com/russross/blackfriday/v2 v2.1.0 // indirect
	github.com/twitchtv/twirp v8.1.3+incompatible // indirect
	github.com/xrash/smetrics v0.0.0-20201216005158-039620a65673 // indirect
	github.com/yutopp/go-amf0 v0.1.0 // indirect
	github.com/zeebo/xxh3 v1.0.2 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	go.uber.org/zap v1.27.0 // indirect
	golang.org/x/crypto v0.19.0 // indirect
	golang.org/x/exp v0.0.0-20240222234643-814bf88cf225 // indirect
	golang.org/x/net v0.21.0 // indirect
	golang.org/x/sync v0.6.0 // indirect
	golang.org/x/sys v0.17.0 // indirect
	golang.org/x/text v0.14.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20240221002015-b0ce06bbee7c // indirect
)
