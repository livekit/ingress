package media

import (
	"errors"
)

var (
	ErrUnsupportedEncodeFormat = errors.New("unsupported mime type for encoder")
	ErrUnableToAddPad          = errors.New("could not add pads to bin")
	ErrInvalidInputDimensions  = errors.New("invalid input media dimensions")
	ErrInvalidInputFPS         = errors.New("invalid input media FPS")
	ErrInvalidOutputDimensions = errors.New("invalid output media dimensions")
)
