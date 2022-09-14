package main

import (
	"context"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/urfave/cli/v2"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/livekit/ingress/pkg/config"
	"github.com/livekit/ingress/pkg/errors"
	"github.com/livekit/ingress/pkg/rtmp"
	"github.com/livekit/ingress/pkg/service"
	"github.com/livekit/ingress/version"
	"github.com/livekit/protocol/ingress"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/redis"
	"github.com/livekit/protocol/tracer"
)

func main() {
	app := &cli.App{
		Name:        "ingress",
		Usage:       "LiveKit Ingress",
		Version:     version.Version,
		Description: "import RTMP to LiveKit",
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
						Name: "ws-url",
					},
					&cli.StringFlag{
						Name: "token",
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

	rpcServer := ingress.NewRedisRPC(livekit.NodeID(conf.NodeID), rc)
	svc := service.NewService(conf, rpcServer)

	err = setupHealthHandlers(conf, svc)
	if err != nil {
		return err
	}

	stopChan := make(chan os.Signal, 1)
	signal.Notify(stopChan, syscall.SIGTERM, syscall.SIGQUIT)

	killChan := make(chan os.Signal, 1)
	signal.Notify(killChan, syscall.SIGINT)

	// Run RTMP server
	rtmpsrv := rtmp.NewRTMPServer()
	relay := rtmp.NewRTMPRelay(rtmpsrv)

	err = rtmpsrv.Start(conf, svc.HandleRTMPPublishRequest)
	if err != nil {
		return err
	}
	err = relay.Start(conf)
	if err != nil {
		return err
	}

	go func() {
		select {
		case sig := <-stopChan:
			logger.Infow("exit requested, finishing recording then shutting down", "signal", sig)
			svc.Stop(false)
			relay.Stop()
			rtmpsrv.Stop()

		case sig := <-killChan:
			logger.Infow("exit requested, stopping recording and shutting down", "signal", sig)
			svc.Stop(true)
			relay.Stop()
			rtmpsrv.Stop()
		}
	}()

	return svc.Run()
}

func setupHealthHandlers(conf *config.Config, svc *service.Service) error {
	if conf.HealthPort == 0 {
		return nil
	}

	healthHttpHandler := func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("Healthy"))
	}

	availabilityHttpHandler := func(w http.ResponseWriter, _ *http.Request) {
		_, canAccept, err := svc.Status()
		if err != nil {
			logger.Errorw("failed to read status", err)
		}

		if !canAccept {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("No availability"))
		}

		_, _ = w.Write([]byte("Available"))
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", healthHttpHandler)
	mux.HandleFunc("/availability", availabilityHttpHandler)

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

	ctx, span := tracer.Start(context.Background(), "Handler.New")
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

	wsUrl := c.String("ws_url")
	token := c.String("token")

	rpcHandler := ingress.NewRedisRPC(livekit.NodeID(conf.NodeID), rc)
	handler := service.NewHandler(conf, rpcHandler)

	killChan := make(chan os.Signal, 1)
	signal.Notify(killChan, syscall.SIGINT)

	go func() {
		sig := <-killChan
		logger.Infow("exit requested, stopping recording and shutting down", "signal", sig)
		handler.Kill()
	}()

	handler.HandleIngress(ctx, info, wsUrl, token)
	return nil
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
