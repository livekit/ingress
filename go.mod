module github.com/livekit/ingress

go 1.25

replace github.com/go-gst/go-gst => github.com/livekit/gst-go v0.0.0-20250701011214-e7f61abd14cb

toolchain go1.25.0

require (
	github.com/Eyevinn/mp4ff v0.48.0
	github.com/aclements/go-moremath v0.0.0-20241023150245-c8bbc672ef66
	github.com/frostbyte73/core v0.1.1
	github.com/go-gst/go-glib v1.4.1-0.20250303082535-35ebad1471fd
	github.com/go-gst/go-gst v1.4.0
	github.com/gorilla/mux v1.8.1
	github.com/livekit/go-rtmp v0.0.0-20230829211117-1c4f5a5c81ed
	github.com/livekit/mageutil v0.0.0-20250511045019-0f1ff63f7731
	github.com/livekit/mediatransportutil v0.0.0-20241220010243-a2bdee945564
	github.com/livekit/protocol v1.42.1-0.20251006173230-a7bc52ad9724
	github.com/livekit/psrpc v0.7.0
	github.com/livekit/server-sdk-go/v2 v2.4.2
	github.com/pion/dtls/v3 v3.0.7
	github.com/pion/interceptor v0.1.40
	github.com/pion/rtcp v1.2.15
	github.com/pion/rtp v1.8.21
	github.com/pion/sdp/v3 v3.0.15
	github.com/pion/webrtc/v4 v4.1.4
	github.com/prometheus/client_golang v1.22.0
	github.com/sirupsen/logrus v1.9.3
	github.com/stretchr/testify v1.10.0
	github.com/yutopp/go-flv v0.3.1
	go.uber.org/atomic v1.11.0
	golang.org/x/image v0.27.0
	google.golang.org/grpc v1.74.2
	google.golang.org/protobuf v1.36.6
	gopkg.in/yaml.v3 v3.0.1
)

require (
	buf.build/go/protovalidate v0.13.1 // indirect
	go.opentelemetry.io/auto/sdk v1.1.0 // indirect
	go.opentelemetry.io/otel v1.37.0 // indirect
	go.opentelemetry.io/otel/metric v1.37.0 // indirect
	go.opentelemetry.io/otel/trace v1.37.0 // indirect
)

require (
	buf.build/gen/go/bufbuild/protovalidate/protocolbuffers/go v1.36.6-20250625184727-c923a0c2a132.1 // indirect
	buf.build/go/protoyaml v0.6.0 // indirect
	cel.dev/expr v0.24.0 // indirect
	github.com/antlr4-go/antlr/v4 v4.13.1 // indirect
	github.com/benbjohnson/clock v1.3.5 // indirect
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/bep/debounce v1.2.1 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/dennwc/iters v1.1.0 // indirect
	github.com/dgryski/go-rendezvous v0.0.0-20200823014737-9f7001d12a5f // indirect
	github.com/fsnotify/fsnotify v1.9.0 // indirect
	github.com/gammazero/deque v1.1.0 // indirect
	github.com/go-gst/go-pointer v0.0.0-20241127163939-ba766f075b4c // indirect
	github.com/go-jose/go-jose/v3 v3.0.4 // indirect
	github.com/go-logr/logr v1.4.3 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/google/cel-go v0.25.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/gorilla/websocket v1.5.3 // indirect
	github.com/hashicorp/errwrap v1.1.0 // indirect
	github.com/hashicorp/go-multierror v1.1.0 // indirect
	github.com/jxskiss/base62 v1.1.0 // indirect
	github.com/klauspost/compress v1.18.0 // indirect
	github.com/klauspost/cpuid/v2 v2.2.11 // indirect
	github.com/lithammer/shortuuid/v4 v4.2.0 // indirect
	github.com/mackerelio/go-osstat v0.2.5 // indirect
	github.com/magefile/mage v1.15.0 // indirect
	github.com/mitchellh/mapstructure v1.5.0 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/nats-io/nats.go v1.43.0 // indirect
	github.com/nats-io/nkeys v0.4.11 // indirect
	github.com/nats-io/nuid v1.0.1 // indirect
	github.com/pion/datachannel v1.5.10 // indirect
	github.com/pion/ice/v4 v4.0.10 // indirect
	github.com/pion/logging v0.2.4 // indirect
	github.com/pion/mdns/v2 v2.0.7 // indirect
	github.com/pion/randutil v0.1.0 // indirect
	github.com/pion/sctp v1.8.39 // indirect
	github.com/pion/srtp/v3 v3.0.7 // indirect
	github.com/pion/stun/v3 v3.0.0 // indirect
	github.com/pion/transport/v3 v3.0.7 // indirect
	github.com/pion/turn/v4 v4.1.1 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/prometheus/client_model v0.6.2 // indirect
	github.com/prometheus/common v0.64.0 // indirect
	github.com/prometheus/procfs v0.16.1 // indirect
	github.com/puzpuzpuz/xsync/v3 v3.5.1 // indirect
	github.com/redis/go-redis/v9 v9.11.0 // indirect
	github.com/stoewer/go-strcase v1.3.1 // indirect
	github.com/twitchtv/twirp v8.1.3+incompatible // indirect
	github.com/urfave/cli/v3 v3.3.8
	github.com/wlynxg/anet v0.0.5 // indirect
	github.com/yutopp/go-amf0 v0.1.0 // indirect
	github.com/zeebo/xxh3 v1.0.2 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	go.uber.org/zap v1.27.0 // indirect
	go.uber.org/zap/exp v0.3.0 // indirect
	golang.org/x/crypto v0.40.0 // indirect
	golang.org/x/exp v0.0.0-20250620022241-b7579e27df2b // indirect
	golang.org/x/net v0.42.0 // indirect
	golang.org/x/sync v0.16.0 // indirect
	golang.org/x/sys v0.34.0 // indirect
	golang.org/x/text v0.27.0 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20250603155806-513f23925822 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20250721164621-a45f3dfb1074 // indirect
)
