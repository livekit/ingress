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
	ErrIngressNotFound         = errors.New("ingress not found")
	ErrServerCapacityExceeded  = errors.New("server capacity exceeded")
	ErrInvalidOutputDimensions = NewInvalidVideoParamsError("invalid output media dimensions")
)

type InvalidIngressError string

type InvalidVideoParamsError string

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
	return "invalid ingress parameters: " + string(s)
}

func NewInvalidVideoParamsError(s string) InvalidVideoParamsError {
	return InvalidVideoParamsError(s)
}

func (s InvalidVideoParamsError) Error() string {
	return "invalid video parameters: " + string(s)
}
