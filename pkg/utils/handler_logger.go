package utils

import (
	"fmt"
	"strings"

	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/logger/medialogutils"
)

const (
	logError = iota
	logWarn
	logInfo
	ignore
)

var actions = map[string]int{
	"0:00:": ignore,
	"turnc": logInfo,
	"ice E": logInfo,
	"SDK 2": logInfo,
}

func NewHandlerLogger(resourceID, ingressID string) *medialogutils.CmdLogger {
	l := logger.GetLogger().WithValues("resourceID", resourceID, "ingressID", ingressID)
	return medialogutils.NewCmdLogger(func(s string) {
		lines := strings.Split(strings.TrimSuffix(s, "\n"), "\n")
		for _, line := range lines {
			if strings.HasSuffix(line, "}") {
				fmt.Println(line)
			} else {
				action := logError
				if len(line) > 5 {
					action = actions[line[:5]]
				}
				switch action {
				case ignore:
					continue
				case logInfo:
					l.Infow(line)
				case logWarn:
					l.Warnw(line, nil)
				case logError:
					l.Errorw(line, nil)
				}
			}
		}
	})
}
