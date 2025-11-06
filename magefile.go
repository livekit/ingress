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

//go:build mage
// +build mage

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"go/build"
	"os"
	"os/exec"
	"strings"

	"github.com/livekit/mageutil"
)

var Default = Build

const (
	imageName  = "livekit/ingress"
	gstVersion = "1.26.7"
	goVersion  = "1.25.0"
)

var plugins = []string{"gstreamer", "gst-plugins-base", "gst-plugins-good", "gst-plugins-bad", "gst-plugins-ugly", "gst-libav"}

type packageInfo struct {
	Dir string
}

func Proto() error {
	ctx := context.Background()
	fmt.Println("generating protobuf")

	// parse go mod output
	pkgOut, err := mageutil.Out(ctx, "go list -json -m github.com/livekit/protocol")
	if err != nil {
		return err
	}
	pi := packageInfo{}
	if err = json.Unmarshal(pkgOut, &pi); err != nil {
		return err
	}

	_, err = mageutil.GetToolPath("protoc")
	if err != nil {
		return err
	}
	protocGoPath, err := mageutil.GetToolPath("protoc-gen-go")
	if err != nil {
		return err
	}
	protocGrpcGoPath, err := mageutil.GetToolPath("protoc-gen-go-grpc")
	if err != nil {
		return err
	}

	// generate grpc-related protos
	return mageutil.RunDir(ctx, "pkg/ipc", fmt.Sprintf(
		"protoc"+
			" --go_out ."+
			" --go-grpc_out ."+
			" --go_opt=paths=source_relative"+
			" --go-grpc_opt=paths=source_relative"+
			" --plugin=go=%s"+
			" --plugin=go-grpc=%s"+
			" -I%s -I=. ipc.proto",
		protocGoPath, protocGrpcGoPath, pi.Dir,
	))
}

func Bootstrap() error {
	brewPrefix, err := getBrewPrefix()
	if err != nil {
		return err
	}

	for _, plugin := range plugins {
		if _, err := os.Stat(fmt.Sprintf("%s%s", brewPrefix, plugin)); err != nil {
			if err = run(fmt.Sprintf("brew install %s", plugin)); err != nil {
				return err
			}
		}
	}

	return nil
}

func Build() error {
	gopath := os.Getenv("GOPATH")
	if gopath == "" {
		gopath = build.Default.GOPATH
	}

	return run(fmt.Sprintf("go build -a -o %s/bin/ingress ./cmd/server", gopath))
}

func Test() error {
	return run("go test -v ./pkg/...")
}

func BuildDocker() error {
	return mageutil.Run(context.Background(),
		fmt.Sprintf("docker pull livekit/gstreamer:%s-dev", gstVersion),
		fmt.Sprintf("docker pull livekit/gstreamer:%s-prod", gstVersion),
		fmt.Sprintf("docker build --no-cache -t %s:latest -f build/ingress/Dockerfile --build-arg GSTVERSION=%s --build-arg GOVERSION=%s .", imageName, gstVersion, goVersion),
	)
}

func BuildDockerLinux() error {
	return mageutil.Run(context.Background(),
		fmt.Sprintf("docker pull livekit/gstreamer:%s-dev", gstVersion),
		fmt.Sprintf("docker pull livekit/gstreamer:%s-prod", gstVersion),
		fmt.Sprintf("docker build --no-cache --platform linux/amd64 -t %s:latest -f build/ingress/Dockerfile --build-arg GSTVERSION=%s --build-arg GOVERSION=%s .", imageName, gstVersion, goVersion),
	)
}

func Integration(configFile string) error {
	if err := Build(); err != nil {
		return err
	}

	return Retest(configFile)
}

func Retest(configFile string) error {
	err := WhipClient()
	if err != nil {
		return err
	}

	cmd := exec.Command("go", "test", "-v", "-count=1", "--tags=integration", "./test/...")

	brewPrefix, err := getBrewPrefix()
	if err != nil {
		return err
	}

	var sb strings.Builder
	sb.WriteString("GST_PLUGIN_PATH=")
	for i, plugin := range plugins {
		if i > 0 {
			sb.WriteString(":")
		}
		sb.WriteString(brewPrefix)
		sb.WriteString(plugin)
	}

	confStr, err := os.ReadFile(configFile)
	if err != nil {
		return err
	}

	cmd.Env = append(os.Environ(), sb.String(), "GST_DEBUG=3", fmt.Sprintf("INGRESS_CONFIG_BODY=%s", string(confStr)))
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func Publish(ingressId string) error {
	return run(fmt.Sprintf("gst-launch-1.0 -v flvmux name=mux ! rtmp2sink location=rtmp://localhost:1935/live/%s  audiotestsrc freq=200 ! faac ! mux.  videotestsrc pattern=ball is-live=true ! video/x-raw,width=1280,height=720 ! x264enc speed-preset=3 ! mux.", ingressId))
}

func WhipClient() error {
	return run("go build -C ./test/livekit-whip-bot/cmd/whip-client/ ./...")
}

// helpers

func getBrewPrefix() (string, error) {
	out, err := exec.Command("brew", "--prefix").Output()
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s/Cellar/", strings.TrimSpace(string(out))), nil
}

func run(commands ...string) error {
	for _, command := range commands {
		args := strings.Split(command, " ")
		if err := runArgs(args...); err != nil {
			return err
		}
	}
	return nil
}

func runArgs(args ...string) error {
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
