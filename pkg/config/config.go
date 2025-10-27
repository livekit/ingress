// Copyright 2023 LiveKit, Inc.
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

package config

import (
	"os"

	"github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"

	"github.com/livekit/ingress/pkg/errors"
	"github.com/livekit/mediatransportutil/pkg/rtcconfig"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/logger/medialogutils"
	"github.com/livekit/protocol/redis"
	"github.com/livekit/protocol/utils"
	"github.com/livekit/psrpc"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

const (
	DefaultRTMPPort      int = 1935
	DefaultWHIPPort          = 8080
	DefaultHTTPRelayPort     = 9090
)

var (
	DefaultICEPortRange = []uint16{2000, 4000}
)

type Config struct {
	*ServiceConfig  `yaml:",inline"`
	*InternalConfig `yaml:",inline"`
}

type ServiceConfig struct {
	Redis     *redis.RedisConfig `yaml:"redis"`      // required
	ApiKey    string             `yaml:"api_key"`    // required (env LIVEKIT_API_KEY)
	ApiSecret string             `yaml:"api_secret"` // required (env LIVEKIT_API_SECRET)
	WsUrl     string             `yaml:"ws_url"`     // required (env LIVEKIT_WS_URL)

	HealthPort       int           `yaml:"health_port"`
	DebugHandlerPort int           `yaml:"debug_handler_port"`
	PrometheusPort   int           `yaml:"prometheus_port"`
	RTMPPort         int           `yaml:"rtmp_port"` // -1 to disable RTMP
	WHIPPort         int           `yaml:"whip_port"` // -1 to disable WHIP
	HTTPRelayPort    int           `yaml:"http_relay_port"`
	Logging          logger.Config `yaml:"logging"`
	Development      bool          `yaml:"development"`
	WHIPProxyEnabled bool          `yaml:"whip_proxy_enabled"` // If true, WHIP requests with transcoding bypassed will be handled by the SFU directly

	// Used for WHIP transport
	RTCConfig rtcconfig.RTCConfig `yaml:"rtc_config"`

	// CPU costs for various ingress types
	CPUCost CPUCostConfig `yaml:"cpu_cost"`

	// Experimental config
	// Reduces ingest e2e latency by dropping excess preroll buffers
	EnableStreamLatencyReduction bool `yaml:"enable_stream_latency_reduction"`
}

type InternalConfig struct {
	// internal
	ServiceName string `yaml:"service_name"`
	NodeID      string `yaml:"node_id"` // Do not provide, will be overwritten
}

type CPUCostConfig struct {
	RTMPCpuCost                  float64 `yaml:"rtmp_cpu_cost"`
	WHIPCpuCost                  float64 `yaml:"whip_cpu_cost"`
	WHIPBypassTranscodingCpuCost float64 `yaml:"whip_bypass_transcoding_cpu_cost"`
	URLCpuCost                   float64 `yaml:"url_cpu_cost"`
	MinIdleRatio                 float64 `yaml:"min_idle_ratio"` // Target idle cpu ratio when deciding availability for new requests
}

func NewConfig(confString string) (*Config, error) {
	conf := &Config{
		ServiceConfig: &ServiceConfig{
			ApiKey:    os.Getenv("LIVEKIT_API_KEY"),
			ApiSecret: os.Getenv("LIVEKIT_API_SECRET"),
			WsUrl:     os.Getenv("LIVEKIT_WS_URL"),
		},
		InternalConfig: &InternalConfig{
			ServiceName: "ingress",
		},
	}
	if confString != "" {
		if err := yaml.Unmarshal([]byte(confString), conf); err != nil {
			return nil, errors.ErrCouldNotParseConfig(err)
		}
	}

	if conf.Redis == nil {
		return nil, psrpc.NewErrorf(psrpc.InvalidArgument, "redis configuration is required")
	}

	return conf, nil
}
func (conf *ServiceConfig) InitDefaults() error {
	if conf.RTMPPort == 0 {
		conf.RTMPPort = DefaultRTMPPort
	}
	if conf.HTTPRelayPort == 0 {
		conf.HTTPRelayPort = DefaultHTTPRelayPort
	}
	if conf.WHIPPort == 0 {
		conf.WHIPPort = DefaultWHIPPort
	}

	err := conf.InitWhipConf()
	if err != nil {
		return err
	}

	return nil
}

func (c *ServiceConfig) InitWhipConf() error {
	if c.WHIPPort <= 0 {
		return nil
	}

	if c.RTCConfig.UDPPort.Start == 0 && c.RTCConfig.ICEPortRangeStart == 0 {
		c.RTCConfig.UDPPort.Start = 7885
	}

	// Validate will set the NodeIP
	err := c.RTCConfig.Validate(c.Development)
	if err != nil {
		return err
	}

	return nil
}

func (conf *Config) Init() error {
	conf.NodeID = utils.NewGuid("NE_")

	err := conf.InitDefaults()
	if err != nil {
		return err
	}

	if err := conf.InitLogger(); err != nil {
		return err
	}

	return nil
}

func (c *Config) InitLogger(values ...interface{}) error {
	zl, err := logger.NewZapLogger(&c.Logging)
	if err != nil {
		return err
	}

	values = append(c.getLoggerValues(), values...)
	l := zl.WithValues(values...)
	logger.SetLogger(l, c.ServiceName)
	lksdk.SetLogger(medialogutils.NewOverrideLogger(nil))

	return nil
}

// To use with zap logger
func (c *Config) getLoggerValues() []interface{} {
	return []interface{}{"nodeID", c.NodeID}
}

// To use with logrus
func (c *Config) GetLoggerFields() logrus.Fields {
	fields := logrus.Fields{
		"logger": c.ServiceName,
	}
	v := c.getLoggerValues()
	for i := 0; i < len(v); i += 2 {
		fields[v[i].(string)] = v[i+1]
	}

	return fields
}
