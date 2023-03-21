package errors

import (
	"errors"

	"github.com/livekit/psrpc"
	"github.com/tinyzimmer/go-gst/gst"
)

var (
	ErrNoConfig                = psrpc.NewErrorf(psrpc.InvalidArgument, "missing config")
	ErrUnsupportedEncodeFormat = psrpc.NewErrorf(psrpc.InvalidArgument, "unsupported mime type for encoder")
	ErrUnableToAddPad          = psrpc.NewErrorf(psrpc.Internal, "could not add pads to bin")
	ErrIngressNotFound         = psrpc.NewErrorf(psrpc.NotFound, "ingress not found")
	ErrServerCapacityExceeded  = psrpc.NewErrorf(psrpc.ResourceExhausted, "server capacity exceeded")
	ErrServerShuttingDown      = psrpc.NewErrorf(psrpc.Unavailable, "server shutting down")
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

func ErrFromGstFlowReturn(ret gst.FlowReturn) error {
	return psrpc.NewErrorf(psrpc.Internal, "GST Flow Error %d (%s)", ret, ret.String())
}

func ErrInvalidIngress(s string) psrpc.Error {
	return psrpc.NewErrorf(psrpc.InvalidArgument, "%s", s)
}

func ErrHttpRelayFailure(statusCode int) {
	// Any failure in the relay between the handler and the service is treated as internal

	psrpc.Errorf(psrpc.Internal, "HTTP request failed with code %d", statusCode)
}
