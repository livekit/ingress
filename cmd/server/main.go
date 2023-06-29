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

	"github.com/urfave/cli/v2"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/livekit/ingress/pkg/config"
	"github.com/livekit/ingress/pkg/errors"
	"github.com/livekit/ingress/pkg/params"
	"github.com/livekit/ingress/pkg/rtmp"
	"github.com/livekit/ingress/pkg/service"
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
	app := &cli.App{
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
						Name: "ws-url",
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
				EnvVars: []string{"INGRESS_CONFIG_FILE"},
			},
			&cli.StringFlag{
				Name:    "config-body",
				Usage:   "LiveKit Ingress yaml config body",
				EnvVars: []string{"INGRESS_CONFIG_BODY"},
			},
		},
		Action: runService,
	}

	if err := app.Run(os.Args); err != nil {
		fmt.Println(err)
	}
}

func runService(c *cli.Context) error {
	conf, err := getConfig(c)
	if err != nil {
		return err
	}

	rc, err := redis.GetRedisClient(conf.Redis)
	if err != nil {
		return err
	}

	bus := psrpc.NewRedisMessageBus(rc)
	psrpcClient, err := rpc.NewIOInfoClient(conf.NodeID, bus)
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
		psrpcWHIPClient, err := rpc.NewIngressHandlerClient(conf.NodeID, bus)
		if err != nil {
			return err
		}

		whipsrv = whip.NewWHIPServer(psrpcWHIPClient)
	}

	svc := service.NewService(conf, psrpcClient, bus, whipsrv)

	_, err = rpc.NewIngressInternalServer(conf.NodeID, svc, bus)
	if err != nil {
		return err
	}

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
		err = whipsrv.Start(conf, svc.HandleWHIPPublishRequest, svc)
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

func runHandler(c *cli.Context) error {
	conf, err := getConfig(c)
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
		HandleIngress(ctx context.Context, info *livekit.IngressInfo, wsUrl, token string, extraParams any)
	}

	bus := psrpc.NewRedisMessageBus(rc)
	rpcClient, err := rpc.NewIOInfoClient(conf.NodeID, bus)
	if err != nil {
		return err
	}
	handler = service.NewHandler(conf, rpcClient)

	setupHandlerRPCHandlers(conf, handler.(*service.Handler), bus, info, ep)

	killChan := make(chan os.Signal, 1)
	signal.Notify(killChan, syscall.SIGINT)

	go func() {
		sig := <-killChan
		logger.Infow("exit requested, stopping all ingress and shutting down", "signal", sig)
		handler.Kill()

		time.Sleep(10 * time.Second)
		// If handler didn't exit cleanly after 10s, cancel the context
		cancel()
	}()

	wsUrl := conf.WsUrl
	if c.String("ws-url") != "" {
		wsUrl = c.String("ws-url")
	}

	handler.HandleIngress(ctx, info, wsUrl, token, ep)
	return nil
}

func setupHandlerRPCHandlers(conf *config.Config, handler *service.Handler, bus psrpc.MessageBus, info *livekit.IngressInfo, ep any) error {
	rpcServer, err := rpc.NewIngressHandlerServer(conf.NodeID, handler, bus)
	if err != nil {
		return err
	}

	return service.RegisterIngressRpcHandlers(rpcServer, info)
}

func getConfig(c *cli.Context) (*config.Config, error) {
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

	return config.NewConfig(configBody)
}
