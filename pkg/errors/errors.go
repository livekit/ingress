package errors

import (
	"errors"
	"fmt"

	"github.com/tinyzimmer/go-gst/gst"
)

var (
	ErrNoConfig = errors.New("missing config")

	ErrUnsupportedEncodeFormat = errors.New("unsupported mime type for encoder")
	ErrUnableToAddPad          = errors.New("could not add pads to bin")
	ErrInvalidInputDimensions  = errors.New("invalid input media dimensions")
	ErrInvalidInputFPS         = errors.New("invalid input media FPS")
	ErrInvalidOutputDimensions = errors.New("invalid output media dimensions")
	ErrIngressNotFound         = errors.New("ingress not found")
	ErrServerCapacityExceeded  = errors.New("server capacity exceeded")
)

type InvalidIngressError string

func New(err string) error {
	return errors.New(err)
}

func ErrCouldNotParseConfig(err error) error {
	return fmt.Errorf("could not parse config: %v", err)
}

func ErrFromGstFlowReturn(ret gst.FlowReturn) error {
	return fmt.Errorf("GST Flow Error %d (%s)", ret, ret.String())
}

func NewInvalidIngressError(s string) InvalidIngressError {
	return InvalidIngressError(s)
}

func (s InvalidIngressError) Error() string {
	return string(s)
}
