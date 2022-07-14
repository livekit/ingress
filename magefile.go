//go:build mage
// +build mage

package main

import (
	"fmt"
	"go/build"
	"os"
	"os/exec"
	"strings"
)

var Default = Test

const (
	rtspServerVersion = "v0.19.2"
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

	if _, err := os.Stat("bin/rtsp-simple-server"); err != nil {
		if _, err := os.Stat("bin"); err != nil {
			os.Mkdir("bin", 0755)
		}

		filename := fmt.Sprintf("rtsp-simple-server_%s_darwin_amd64.tar.gz", rtspServerVersion)
		err = run(
			fmt.Sprintf("wget https://github.com/aler9/rtsp-simple-server/releases/download/%s/%s", rtspServerVersion, filename),
			fmt.Sprintf("tar -zxvf %s", filename),
			fmt.Sprintf("rm %s", filename),
			"mv rtsp-simple-server bin/",
			"mv rtsp-simple-server.yml bin/",
		)
		if err != nil {
			return err
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
	if err := Build(); err != nil {
		return err
	}

	return Retest()
}

func Retest() error {
	cmd := exec.Command("go", "test", "-v", "-count=1", "./test/...")

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

	cmd.Env = append(os.Environ(), sb.String(), "GST_DEBUG=3")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func Publish() error {
	cmd := exec.Command("./rtsp-simple-server")
	cmd.Dir = "bin"
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	return run("gst-launch-1.0 -v videotestsrc pattern=ball ! video/x-raw,width=1280,height=720 ! x264enc ! flvmux ! rtmp2sink location=rtmp://localhost:1935/live/stream1")
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
