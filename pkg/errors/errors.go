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

package errors

import (
	"errors"
	"io"

	"github.com/go-gst/go-gst/gst"
	"google.golang.org/grpc/status"

	"github.com/livekit/psrpc"
)

var (
	ErrNoConfig                     = psrpc.NewErrorf(psrpc.InvalidArgument, "missing config")
	ErrInvalidAudioOptions          = psrpc.NewErrorf(psrpc.InvalidArgument, "invalid audio options")
	ErrInvalidVideoOptions          = psrpc.NewErrorf(psrpc.InvalidArgument, "invalid video options")
	ErrInvalidAudioPreset           = psrpc.NewErrorf(psrpc.InvalidArgument, "invalid audio encoding preset")
	ErrInvalidVideoPreset           = psrpc.NewErrorf(psrpc.InvalidArgument, "invalid video encoding preset")
	ErrSourceNotReady               = psrpc.NewErrorf(psrpc.FailedPrecondition, "source encoder not ready")
	ErrUnsupportedDecodeFormat      = psrpc.NewErrorf(psrpc.NotAcceptable, "unsupported format for the source media")
	ErrUnsupportedEncodeFormat      = psrpc.NewErrorf(psrpc.InvalidArgument, "unsupported mime type for encoder")
	ErrUnsupportedURLFormat         = psrpc.NewErrorf(psrpc.InvalidArgument, "unsupported URL type")
	ErrDuplicateTrack               = psrpc.NewErrorf(psrpc.NotAcceptable, "more than 1 track with given media kind")
	ErrUnableToAddPad               = psrpc.NewErrorf(psrpc.Internal, "could not add pads to bin")
	ErrMissingResourceId            = psrpc.NewErrorf(psrpc.InvalidArgument, "missing resource ID")
	ErrInvalidRelayToken            = psrpc.NewErrorf(psrpc.PermissionDenied, "invalid token")
	ErrIngressNotFound              = psrpc.NewErrorf(psrpc.NotFound, "ingress not found")
	ErrServerCapacityExceeded       = psrpc.NewErrorf(psrpc.ResourceExhausted, "server capacity exceeded")
	ErrServerShuttingDown           = psrpc.NewErrorf(psrpc.Unavailable, "server shutting down")
	ErrIngressClosing               = psrpc.NewErrorf(psrpc.Unavailable, "ingress closing")
	ErrMissingStreamKey             = psrpc.NewErrorf(psrpc.InvalidArgument, "missing stream key")
	ErrPrerollBufferReset           = psrpc.NewErrorf(psrpc.Internal, "preroll buffer reset")
	ErrInvalidSimulcast             = psrpc.NewErrorf(psrpc.NotAcceptable, "invalid simulcast configuration")
	ErrSimulcastTranscode           = psrpc.NewErrorf(psrpc.NotAcceptable, "simulcast is not supported when transcoding")
	ErrRoomDisconnected             = psrpc.NewErrorf(psrpc.NotAcceptable, "room disconnected")
	ErrInvalidWHIPRestartRequest    = psrpc.NewErrorf(psrpc.InvalidArgument, "whip restart request was invalid")
	ErrRoomDisconnectedUnexpectedly = RetryableError{psrpc.NewErrorf(psrpc.Unavailable, "room disconnected unexpectedly")}
)

type RetryableError struct {
	psrpcErr psrpc.Error
}

func (err RetryableError) Error() string {
	return err.psrpcErr.Error()
}

func (err RetryableError) Code() psrpc.ErrorCode {
	return err.psrpcErr.Code()
}

func (err RetryableError) ToHttp() int {
	return err.psrpcErr.ToHttp()
}

func (err RetryableError) GRPCStatus() *status.Status {
	return err.psrpcErr.GRPCStatus()
}

func (err RetryableError) Unwrap() error {
	return err.psrpcErr
}

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

func ErrUnsupportedDecodeMimeType(mimeType string) psrpc.Error {
	return psrpc.NewErrorf(psrpc.NotAcceptable, "unsupported mime type (%s) for the source media", mimeType)
}

func ErrorToGstFlowReturn(err error) gst.FlowReturn {
	switch {
	case err == nil:
		return gst.FlowOK
	case errors.Is(err, io.EOF):
		return gst.FlowEOS
	default:
		return gst.FlowError
	}
}
