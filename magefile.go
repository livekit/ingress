//go:build mage
// +build mage

package main

import (
	"context"
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
	gstVersion = "1.20.4"
)

var plugins = []string{"gstreamer", "gst-plugins-base", "gst-plugins-good", "gst-plugins-bad", "gst-plugins-ugly", "gst-libav"}

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

func BuildDocker() error {
	return mageutil.Run(context.Background(),
		fmt.Sprintf("docker pull livekit/gstreamer:%s-dev", gstVersion),
		fmt.Sprintf("docker pull livekit/gstreamer:%s-prod", gstVersion),
		fmt.Sprintf("docker build --no-cache -t %s:latest -f build/Dockerfile .", imageName),
	)
}

func BuildDockerLinux() error {
	return mageutil.Run(context.Background(),
		fmt.Sprintf("docker pull livekit/gstreamer:%s-dev", gstVersion),
		fmt.Sprintf("docker pull livekit/gstreamer:%s-prod", gstVersion),
		fmt.Sprintf("docker build --no-cache --platform linux/amd64 -t %s:latest -f build/Dockerfile .", imageName),
	)
}

func Integration(configFile string) error {
	if err := Build(); err != nil {
		return err
	}

	return Retest(configFile)
}

func Retest(configFile string) error {
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

	cmd.Env = append(os.Environ(), sb.String(), "GST_DEBUG=3", fmt.Sprintf("INGRESS_CONFIG_FILE=%s", configFile))
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func Publish(ingressId string) error {
	return run(fmt.Sprintf("gst-launch-1.0 -v flvmux name=mux ! rtmp2sink location=rtmp://localhost:1935/live/%s  audiotestsrc freq=200 ! faac ! mux.  videotestsrc pattern=ball is-live=true ! video/x-raw,width=1280,height=720 ! x264enc speed-preset=3 ! mux.", ingressId))
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
