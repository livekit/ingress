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
	"google.golang.org/protobuf/proto"

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
						Name: "request",
					},
					&cli.StringFlag{
						Name: "url",
					},
					&cli.StringFlag{
						Name: "config-body",
					},
				},
				Action: runHandler,
				Hidden: true,
			},
			{
				Name:        "test",
				Description: "runs tests",
				Action:      runTests,
				Hidden:      true,
			},
		},
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "config",
				Usage:   "LiveKit Egress yaml config file",
				EnvVars: []string{"INGRESS_CONFIG_FILE"},
			},
			&cli.StringFlag{
				Name:    "config-body",
				Usage:   "LiveKit Egress yaml config body",
				EnvVars: []string{"INGRESS_CONFIG_BODY"},
			},
		},
		Action: runService,
	}

	if err := app.Run(os.Args); err != nil {
		fmt.Println(err)
	}
}

type httpHandler struct {
	svc *service.Service
}

func (h *httpHandler) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	info, err := h.svc.Status()
	if err != nil {
		logger.Errorw("failed to read status", err)
	}

	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(info)
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

	rpcServer := ingress.NewRedisRPCServer(rc)
	svc := service.NewService(conf, rpcServer)

	if conf.HealthPort != 0 {
		go func() {
			_ = http.ListenAndServe(fmt.Sprintf(":%d", conf.HealthPort), &httpHandler{svc: svc})
		}()
	}

	stopChan := make(chan os.Signal, 1)
	signal.Notify(stopChan, syscall.SIGTERM, syscall.SIGQUIT)

	killChan := make(chan os.Signal, 1)
	signal.Notify(killChan, syscall.SIGINT)

	// Run RTMP server
	rtmpsrv := rtmp.NewRTMPServer()
	relay := rtmp.NewRTMPRelay(rtmpsrv)

	err = rtmpsrv.Start(conf)
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

	req := &livekit.StartIngressRequest{}
	reqString := c.String("request")
	err = proto.Unmarshal([]byte(reqString), req)
	if err != nil {
		span.RecordError(err)
		return err
	}

	rpcHandler := ingress.NewRedisRPCServer(rc)
	handler := service.NewHandler(conf, rpcHandler)

	killChan := make(chan os.Signal, 1)
	signal.Notify(killChan, syscall.SIGINT)

	go func() {
		sig := <-killChan
		logger.Infow("exit requested, stopping recording and shutting down", "signal", sig)
		handler.Kill()
	}()

	url := c.String("url")
	if url == "" {
		return errors.New("url missing")
	}

	handler.HandleRequest(ctx, req, url)
	return nil
}

func runTests(c *cli.Context) error {
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
