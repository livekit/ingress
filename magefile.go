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
	"path/filepath"
	"strings"
	"time"

	"github.com/livekit/mageutil"
)

var Default = Build

const (
	imageName      = "livekit/ingress"
	gstVersionFile = ".gst-version"
	composeFile    = "build/test/compose.yaml"
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
		protocGoPath, protocGrpcGoPath, pi.Dir+"/protobufs",
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

func Lint() error {
	return run("go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.11.3",
		"golangci-lint run --timeout=5m")
}

func BuildDocker() error {
	// Use the current day (not a per-second timestamp) so the security refresh
	// build arg is stable within a day and Docker layer caching avoids fetching
	// updates on every local build.
	securityRefresh := time.Now().UTC().Format("20060102")

	gstVersion, err := getGstVersion()
	if err != nil {
		return err
	}

	return mageutil.Run(context.Background(),
		fmt.Sprintf("docker pull livekit/gstreamer:%s-dev", gstVersion),
		fmt.Sprintf("docker pull livekit/gstreamer:%s-prod", gstVersion),
		fmt.Sprintf("docker build --no-cache -t %s:latest -f build/ingress/Dockerfile --build-arg GSTVERSION=%s --build-arg SECURITY_REFRESH=%s .", imageName, gstVersion, securityRefresh),
	)
}

func BuildDockerLinux() error {
	// Use the current day (not a per-second timestamp) so the security refresh
	// build arg is stable within a day and Docker layer caching avoids fetching
	// updates on every local build.
	securityRefresh := time.Now().UTC().Format("20060102")

	gstVersion, err := getGstVersion()
	if err != nil {
		return err
	}

	return mageutil.Run(context.Background(),
		fmt.Sprintf("docker pull livekit/gstreamer:%s-dev", gstVersion),
		fmt.Sprintf("docker pull livekit/gstreamer:%s-prod", gstVersion),
		fmt.Sprintf("docker build --no-cache --platform linux/amd64 -t %s:latest -f build/ingress/Dockerfile --build-arg GSTVERSION=%s --build-arg SECURITY_REFRESH=%s .", imageName, gstVersion, securityRefresh),
	)
}

func Integration(configFile string) error {
	if err := Build(); err != nil {
		return err
	}

	return Retest(configFile)
}

// IntegrationDocker runs the integration suite the way CI does: the suite and
// the Redis and room server it needs, all in containers. Unlike Integration it
// needs no local GStreamer, and it runs the same images CI runs, so a pass
// here means the same thing a green check does.
//
// The config has to reach the services by their compose names, redis:6379 and
// ws://livekit:7880, rather than localhost.
func IntegrationDocker(configFile string) error {
	abs, err := filepath.Abs(configFile)
	if err != nil {
		return err
	}

	gstVersion, err := getGstVersion()
	if err != nil {
		return err
	}

	env := append(os.Environ(),
		fmt.Sprintf("INGRESS_TEST_CONFIG=%s", abs),
		fmt.Sprintf("GSTVERSION=%s", gstVersion),
		fmt.Sprintf("SECURITY_REFRESH=%s", time.Now().UTC().Format("20060102")),
	)

	// run leaves the services it started behind, so they are torn down here
	// whatever the suite did.
	defer func() {
		down := exec.Command("docker", "compose", "-f", composeFile, "down", "-v")
		down.Env = env
		down.Stdout = os.Stdout
		down.Stderr = os.Stderr
		_ = down.Run()
	}()

	// --build because run reuses whatever image already exists, which would
	// silently test a stale one after any source or Dockerfile change.
	cmd := exec.Command("docker", "compose", "-f", composeFile, "run", "--build", "--rm", "test")
	cmd.Env = env
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
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

// getGstVersion returns the GStreamer version pinned in .gst-version, the single source of
// truth shared with CI and with the Dockerfiles, which take it as the GSTVERSION build arg.
func getGstVersion() (string, error) {
	b, err := os.ReadFile(gstVersionFile)
	if err != nil {
		return "", err
	}

	v := strings.TrimSpace(string(b))
	if v == "" {
		return "", fmt.Errorf("%s is empty", gstVersionFile)
	}

	return v, nil
}

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
