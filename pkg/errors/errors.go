package errors

import (
	"errors"
	"io"

	"github.com/tinyzimmer/go-gst/gst"

	"github.com/livekit/psrpc"
)

var (
	ErrNoConfig                = psrpc.NewErrorf(psrpc.InvalidArgument, "missing config")
	ErrInvalidAudioOptions     = psrpc.NewErrorf(psrpc.InvalidArgument, "invalid audio options")
	ErrInvalidVideoOptions     = psrpc.NewErrorf(psrpc.InvalidArgument, "invalid video options")
	ErrInvalidAudioPreset      = psrpc.NewErrorf(psrpc.InvalidArgument, "invalid audio encoding preset")
	ErrInvalidVideoPreset      = psrpc.NewErrorf(psrpc.InvalidArgument, "invalid video encoding preset")
	ErrSourceNotReady          = psrpc.NewErrorf(psrpc.FailedPrecondition, "source encoder not ready")
	ErrUnsupportedDecodeFormat = psrpc.NewErrorf(psrpc.NotAcceptable, "unsupported mime type for the source media")
	ErrUnsupportedEncodeFormat = psrpc.NewErrorf(psrpc.InvalidArgument, "unsupported mime type for encoder")
	ErrDuplicateTrack          = psrpc.NewErrorf(psrpc.NotAcceptable, "more than 1 track with given media kind")
	ErrUnableToAddPad          = psrpc.NewErrorf(psrpc.Internal, "could not add pads to bin")
	ErrIngressNotFound         = psrpc.NewErrorf(psrpc.NotFound, "ingress not found")
	ErrServerCapacityExceeded  = psrpc.NewErrorf(psrpc.ResourceExhausted, "server capacity exceeded")
	ErrServerShuttingDown      = psrpc.NewErrorf(psrpc.Unavailable, "server shutting down")
	ErrMissingStreamKey        = psrpc.NewErrorf(psrpc.InvalidArgument, "missing stream key")
	ErrPrerollBufferReset      = psrpc.NewErrorf(psrpc.Internal, "preroll buffer reset")
)

func New(err string) error {
	return errors.New(err)
}

func Is(err, target error) bool {
	return errors.Is(err, target)
}

func As(err error, target any) bool {
	return errors.As(err, target)
}

func ErrCouldNotParseConfig(err error) psrpc.Error {
	return psrpc.NewErrorf(psrpc.InvalidArgument, "could not parse config: %v", err)
}

func ErrFromGstFlowReturn(ret gst.FlowReturn) psrpc.Error {
	return psrpc.NewErrorf(psrpc.Internal, "GST Flow Error %d (%s)", ret, ret.String())
}

func ErrHttpRelayFailure(statusCode int) psrpc.Error {
	// Any failure in the relay between the handler and the service is treated as internal

	return psrpc.NewErrorf(psrpc.Internal, "HTTP request failed with code %d", statusCode)
}

func ErrorToGstFlowReturn(err error) gst.FlowReturn {
	switch errors.Unwrap(err) {
	case nil:
		return gst.FlowOK
	case io.EOF:
		return gst.FlowEOS
	default:
		return gst.FlowError
	}
}
