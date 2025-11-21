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

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/urfave/cli/v3"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/livekit/ingress/pkg/config"
	"github.com/livekit/ingress/pkg/errors"
	"github.com/livekit/ingress/pkg/params"
	"github.com/livekit/ingress/pkg/rtmp"
	"github.com/livekit/ingress/pkg/service"
	"github.com/livekit/ingress/pkg/utils"
	"github.com/livekit/ingress/pkg/whip"
	"github.com/livekit/ingress/version"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/redis"
	"github.com/livekit/protocol/rpc"
	"github.com/livekit/protocol/tracer"
	"github.com/livekit/psrpc"
)

func main() {
	cmd := &cli.Command{
		Name:        "ingress",
		Usage:       "LiveKit Ingress",
		Version:     version.Version,
		Description: "import streamed media to LiveKit",
		Commands: []*cli.Command{
			{
				Name:        "run-handler",
				Description: "runs a request in a new process",
				Flags: []cli.Flag{
					&cli.StringFlag{
						Name: "info",
					},
					&cli.StringFlag{
						Name: "config-body",
					},
					&cli.StringFlag{
						Name: "token",
					},
					&cli.StringFlag{
						Name: "relay-token",
					},
					&cli.StringFlag{
						Name: "ws-url",
					},
					&cli.StringFlag{
						Name: "feature-flags",
					},
					&cli.StringFlag{
						Name: "logging-fields",
					},
					&cli.StringFlag{
						Name: "extra-params",
					},
				},
				Action: runHandler,
				Hidden: true,
			},
		},
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "config",
				Usage:   "LiveKit Ingress yaml config file",
				Sources: cli.EnvVars("INGRESS_CONFIG_FILE"),
			},
			&cli.StringFlag{
				Name:    "config-body",
				Usage:   "LiveKit Ingress yaml config body",
				Sources: cli.EnvVars("INGRESS_CONFIG_BODY"),
			},
		},
		Action: runService,
	}

	if err := cmd.Run(context.Background(), os.Args); err != nil {
		logger.Infow("process excited", "error", err)
	}
}

func runService(_ context.Context, c *cli.Command) error {
	conf, err := getConfig(c, true)
	if err != nil {
		return err
	}

	rc, err := redis.GetRedisClient(conf.Redis)
	if err != nil {
		return err
	}

	bus := psrpc.NewRedisMessageBus(rc)
	psrpcClient, err := rpc.NewIOInfoClient(bus)
	if err != nil {
		return err
	}

	stopChan := make(chan os.Signal, 1)
	signal.Notify(stopChan, syscall.SIGTERM, syscall.SIGQUIT)

	killChan := make(chan os.Signal, 1)
	signal.Notify(killChan, syscall.SIGINT)

	var rtmpsrv *rtmp.RTMPServer
	var whipsrv *whip.WHIPServer
	if conf.RTMPPort > 0 {
		// Run RTMP server
		rtmpsrv = rtmp.NewRTMPServer()
	}
	if conf.WHIPPort > 0 {
		whipsrv, err = whip.NewWHIPServer(bus)
		if err != nil {
			return err
		}
	}

	svc, err := service.NewService(conf, psrpcClient, bus, rtmpsrv, whipsrv, service.NewCmd, "")
	if err != nil {
		return err
	}

	svc.StartDebugHandlers()

	err = setupHealthHandlers(conf, svc)
	if err != nil {
		return err
	}

	relay := service.NewRelay(rtmpsrv, whipsrv)

	if rtmpsrv != nil {
		err = rtmpsrv.Start(conf, svc.HandleRTMPPublishRequest)
		if err != nil {
			return err
		}
	}
	if whipsrv != nil {
		err = whipsrv.Start(conf, svc.HandleWHIPPublishRequest, svc.GetWhipProxyEnabled, svc.GetHealthHandlers())
		if err != nil {
			return err
		}
	}

	err = relay.Start(conf)
	if err != nil {
		return err
	}

	go func() {
		select {
		case sig := <-stopChan:
			logger.Infow("exit requested, finishing all ingress then shutting down", "signal", sig)
			svc.Stop(false)

		case sig := <-killChan:
			logger.Infow("exit requested, stopping all ingress and shutting down", "signal", sig)
			svc.Stop(true)
			relay.Stop()
			if rtmpsrv != nil {
				rtmpsrv.Stop()
			}
			if whipsrv != nil {
				whipsrv.Stop()
			}

		}
	}()

	return svc.Run()
}

func setupHealthHandlers(conf *config.Config, svc *service.Service) error {
	if conf.HealthPort == 0 {
		return nil
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", svc.HealthHandler)
	mux.HandleFunc("/availability", svc.AvailabilityHandler)

	go func() {
		_ = http.ListenAndServe(fmt.Sprintf(":%d", conf.HealthPort), mux)
	}()

	return nil
}

func runHandler(_ context.Context, c *cli.Command) error {
	conf, err := getConfig(c, false)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ctx, span := tracer.Start(ctx, "Handler.New")
	defer span.End()
	logger.Debugw("handler launched")

	rc, err := redis.GetRedisClient(conf.Redis)
	if err != nil {
		span.RecordError(err)
		return err
	}

	info := &livekit.IngressInfo{}
	infoString := c.String("info")
	err = protojson.Unmarshal([]byte(infoString), info)
	if err != nil {
		span.RecordError(err)
		return err
	}

	extraParams := c.String("extra-params")
	var ep any
	switch info.InputType {
	case livekit.IngressInput_WHIP_INPUT:
		whipParams := params.WhipExtraParams{}
		err := json.Unmarshal([]byte(extraParams), &whipParams)
		if err != nil {
			return err
		}
		ep = &whipParams
	}

	token := c.String("token")

	var handler interface {
		Kill()
		HandleIngress(ctx context.Context, info *livekit.IngressInfo, wsUrl, token, relayToken string, featureFlags map[string]string, loggingFields map[string]string, extraParams any) error
	}

	bus := psrpc.NewRedisMessageBus(rc)
	rpcClient, err := rpc.NewIOInfoClient(bus)
	if err != nil {
		return err
	}
	handler = service.NewHandler(conf, rpcClient)

	setupHandlerRPCHandlers(conf, handler.(*service.Handler), bus, info, ep)

	killChan := make(chan os.Signal, 1)
	signal.Notify(killChan, syscall.SIGINT)

	wsUrl := conf.WsUrl
	if c.String("ws-url") != "" {
		wsUrl = c.String("ws-url")
	}

	var featureFlags map[string]string
	if c.String("feature-flags") != "" {
		err = json.Unmarshal([]byte(c.String("feature-flags")), &featureFlags)
		if err != nil {
			return err
		}
	}

	var loggingFields map[string]string
	if c.String("logging-fields") != "" {
		err = json.Unmarshal([]byte(c.String("logging-fields")), &loggingFields)
		if err != nil {
			return err
		}
	}

	params.InitLogger(conf, info, loggingFields)

	go func() {
		sig := <-killChan
		logger.Infow("exit requested, stopping all ingress and shutting down", "signal", sig)
		handler.Kill()

		time.Sleep(10 * time.Second)
		// If handler didn't exit cleanly after 10s, cancel the context
		cancel()
	}()

	err = handler.HandleIngress(ctx, info, wsUrl, token, c.String("relay-token"), featureFlags, loggingFields, ep)
	return translateRetryableError(err)
}

func translateRetryableError(err error) error {
	retrErr := errors.RetryableError{}

	if errors.As(err, &retrErr) {
		return cli.Exit(err, 1)
	}

	return err
}

func setupHandlerRPCHandlers(conf *config.Config, handler *service.Handler, bus psrpc.MessageBus, info *livekit.IngressInfo, ep any) error {
	rpcServer, err := rpc.NewIngressHandlerServer(handler, bus)
	if err != nil {
		return err
	}

	return utils.RegisterIngressRpcHandlers(rpcServer, info)
}

func getConfig(c *cli.Command, initialize bool) (*config.Config, error) {
	configFile := c.String("config")
	configBody := c.String("config-body")
	if configBody == "" {
		if configFile == "" {
			return nil, errors.ErrNoConfig
		}
		content, err := ioutil.ReadFile(configFile)
		if err != nil {
			return nil, err
		}
		configBody = string(content)
	}

	conf, err := config.NewConfig(configBody)
	if err != nil {
		return nil, err
	}

	if initialize {
		err = conf.Init()
		if err != nil {
			return nil, err
		}
	}

	return conf, nil
}
