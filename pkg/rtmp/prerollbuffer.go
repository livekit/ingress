package rtmp

import (
	"bytes"
	"io"
	"sync"

	"github.com/livekit/ingress/pkg/errors"
)

const (
	maxBufferSize = 10000000
)

type prerollBuffer struct {
	lock   sync.Mutex
	buffer *bytes.Buffer
	w      io.WriteCloser

	onBufferReset func() error
}

func newPrerollBuffer(onBufferReset func() error) *prerollBuffer {
	return &prerollBuffer{
		buffer:        &bytes.Buffer{},
		onBufferReset: onBufferReset,
	}
}

func (pb *prerollBuffer) setWriter(w io.WriteCloser) error {
	pb.lock.Lock()
	defer pb.lock.Unlock()

	pb.w = w
	if pb.w != nil {
		_, err := io.Copy(pb.w, pb.buffer)
		if err != nil {
			return err
		}
	}

	return nil
}

func (pb *prerollBuffer) Write(p []byte) (int, error) {
	pb.lock.Lock()
	defer pb.lock.Unlock()

	if pb.w == nil {
		if len(p)+pb.buffer.Len() > maxBufferSize {
			// We would overflow the max allowed buffer size. Reset th buffer state
			pb.buffer.Reset()
			if pb.onBufferReset != nil {
				if err := pb.onBufferReset(); err != nil {
					return 0, err
				}
			}
			return 0, errors.ErrPrerollBufferReset
		}
		return pb.buffer.Write(p)
	}

	return pb.w.Write(p)
}

func (pb *prerollBuffer) Close() error {
	pb.lock.Lock()
	defer pb.lock.Unlock()

	if pb.w != nil {
		return pb.w.Close()
	}

	return nil
}
