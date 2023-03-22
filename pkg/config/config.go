package config

import (
	"os"

	"github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"

	"github.com/livekit/ingress/pkg/errors"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/redis"
	"github.com/livekit/protocol/utils"
	"github.com/livekit/psrpc"
	lksdk "github.com/livekit/server-sdk-go"
)

const (
	DefaultRTMPPort      int = 1935
	DefaultHTTPRelayPort     = 9090
)

type Config struct {
	Redis     *redis.RedisConfig `yaml:"redis"`      // required
	ApiKey    string             `yaml:"api_key"`    // required (env LIVEKIT_API_KEY)
	ApiSecret string             `yaml:"api_secret"` // required (env LIVEKIT_API_SECRET)
	WsUrl     string             `yaml:"ws_url"`     // required (env LIVEKIT_WS_URL)

	HealthPort     int           `yaml:"health_port"`
	PrometheusPort int           `yaml:"prometheus_port"`
	RTMPPort       int           `yaml:"rtmp_port"`
	HTTPRelayPort  int           `yaml:"http_relay_port"`
	Logging        logger.Config `yaml:"logging"`

	// CPU costs for various ingress types
	CPUCost CPUCostConfig `yaml:"cpu_cost"`

	// internal
	NodeID string `yaml:"-"`
}

type CPUCostConfig struct {
	RTMPCpuCost float64 `yaml:"rtmp_cpu_cost"`
}

func NewConfig(confString string) (*Config, error) {
	conf := &Config{
		ApiKey:    os.Getenv("LIVEKIT_API_KEY"),
		ApiSecret: os.Getenv("LIVEKIT_API_SECRET"),
		WsUrl:     os.Getenv("LIVEKIT_WS_URL"),
		NodeID:    utils.NewGuid("NE_"),
	}
	if confString != "" {
		if err := yaml.Unmarshal([]byte(confString), conf); err != nil {
			return nil, errors.ErrCouldNotParseConfig(err)
		}
	}

	if conf.RTMPPort == 0 {
		conf.RTMPPort = DefaultRTMPPort
	}
	if conf.HTTPRelayPort == 0 {
		conf.HTTPRelayPort = DefaultHTTPRelayPort
	}

	if conf.Redis == nil {
		return nil, psrpc.NewErrorf(psrpc.InvalidArgument, "redis configuration is required")
	}

	if err := conf.InitLogger(); err != nil {
		return nil, err
	}

	return conf, nil
}

func (c *Config) InitLogger(values ...interface{}) error {
	zl, err := logger.NewZapLogger(&c.Logging)
	if err != nil {
		return err
	}

	values = append(c.GetLoggerValues(), values...)
	l := zl.WithValues(values...)
	logger.SetLogger(l, "ingress")
	lksdk.SetLogger(l)

	return nil
}

// To use with zap logger
func (c *Config) GetLoggerValues() []interface{} {
	return []interface{}{"nodeID", c.NodeID}
}

// To use with logrus
func (c *Config) GetLoggerFields() logrus.Fields {
	fields := logrus.Fields{
		"logger": "ingress",
	}
	v := c.GetLoggerValues()
	for i := 0; i < len(v); i += 2 {
		fields[v[i].(string)] = v[i+1]
	}

	return fields
}
