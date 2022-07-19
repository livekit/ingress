package config

import (
	"os"

	"github.com/go-logr/zapr"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/yaml.v3"

	"github.com/livekit/ingress/pkg/errors"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/redis"
	"github.com/livekit/protocol/utils"
)

const (
	defaultRTMPPort      int = 1935
	defaultHTTPRelayPort     = 9090
)

type Config struct {
	Redis     *redis.RedisConfig `yaml:"redis"`      // required
	ApiKey    string             `yaml:"api_key"`    // required (env LIVEKIT_API_KEY)
	ApiSecret string             `yaml:"api_secret"` // required (env LIVEKIT_API_SECRET)
	WsUrl     string             `yaml:"ws_url"`     // required (env LIVEKIT_WS_URL)

	HealthPort     int    `yaml:"health_port"`
	PrometheusPort int    `yaml:"prometheus_port"`
	RTMPPort       int    `yaml:"rtmp_port"`
	HTTPRelayPort  int    `yaml:"http_relay_port"`
	LogLevel       string `yaml:"log_level"`

	// internal
	NodeID string `yaml:"-"`
	Url    string `yaml:"url"`
}

func NewConfig(confString string) (*Config, error) {
	conf := &Config{
		LogLevel:  "info",
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
		conf.RTMPPort = defaultRTMPPort
	}
	if conf.HTTPRelayPort == 0 {
		conf.HTTPRelayPort = defaultHTTPRelayPort
	}

	conf.InitLogger()
	return conf, nil
}

func (c *Config) InitLogger() {
	conf := zap.NewProductionConfig()
	if c.LogLevel != "" {
		lvl := zapcore.Level(0)
		if err := lvl.UnmarshalText([]byte(c.LogLevel)); err == nil {
			conf.Level = zap.NewAtomicLevelAt(lvl)
		}
	}

	l, _ := conf.Build()
	logger.SetLogger(zapr.NewLogger(l).WithValues("nodeID", c.NodeID), "ingress")
}
