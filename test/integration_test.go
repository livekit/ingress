package test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/livekit/protocol/redis"
	"github.com/livekit/psrpc"
)

func TestIngress(t *testing.T) {
	conf := getConfig(t)

	rc, err := redis.GetRedisClient(conf.Redis)
	require.NoError(t, err)
	require.NotNil(t, rc, "redis required")

	bus := psrpc.NewRedisMessageBus(rc)

	RunTestSuite(t, conf, bus)
}
