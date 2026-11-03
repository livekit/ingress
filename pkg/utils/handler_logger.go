package utils

import (
	"bytes"
	"fmt"
	"strings"
	"sync"

	"github.com/livekit/protocol/logger"
)

const (
	logError = iota
	logWarn
	logInfo
	ignore
)

const maxLineLength = 8 << 20

var actions = map[string]int{
	"0:00:": ignore,
	"turnc": logInfo,
	"ice E": logInfo,
	"SDK 2": logInfo,
}

// HandlerLogger routes the handler process output line by line. A line split
// across Write calls is held until its newline arrives or Close is called.
type HandlerLogger struct {
	l    logger.Logger
	sink func(line string)

	mu  sync.Mutex
	buf []byte
}

func NewHandlerLogger(resourceID, ingressID string) *HandlerLogger {
	return NewHandlerLoggerWithSink(resourceID, ingressID, nil)
}

// NewHandlerLoggerWithSink sends the handler's own structured lines to sink. A
// nil sink writes them to stdout.
func NewHandlerLoggerWithSink(resourceID, ingressID string, sink func(line string)) *HandlerLogger {
	return &HandlerLogger{
		l:    logger.GetLogger().WithValues("resourceID", resourceID, "ingressID", ingressID),
		sink: sink,
	}
}

func (h *HandlerLogger) Write(p []byte) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	// The held bytes contain no newline, so only the new chunk needs scanning.
	off := len(h.buf)
	h.buf = append(h.buf, p...)
	for {
		idx := bytes.IndexByte(h.buf[off:], '\n')
		if idx < 0 {
			break
		}
		idx += off
		h.processLine(string(h.buf[:idx]))
		h.buf = h.buf[idx+1:]
		off = 0
	}
	if len(h.buf) >= maxLineLength {
		h.l.Warnw("handler output line too long, splitting it", nil, "bytes", len(h.buf))
		h.processLine(string(h.buf))
		h.buf = nil
	}
	if len(h.buf) == 0 {
		h.buf = nil
	}
	return len(p), nil
}

// Close processes any trailing line that has no newline.
func (h *HandlerLogger) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()

	if len(h.buf) > 0 {
		h.processLine(string(h.buf))
		h.buf = nil
	}
	return nil
}

func (h *HandlerLogger) processLine(line string) {
	if line == "" {
		return
	}
	if strings.HasSuffix(line, "}") {
		if h.sink != nil {
			h.sink(line)
		} else {
			fmt.Println(line)
		}
		return
	}

	action := logError
	if len(line) > 5 {
		action = actions[line[:5]]
	}
	switch action {
	case ignore:
	case logInfo:
		h.l.Infow(line)
	case logWarn:
		h.l.Warnw(line, nil)
	case logError:
		h.l.Errorw(line, nil)
	}
}
