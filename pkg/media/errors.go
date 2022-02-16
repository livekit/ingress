package media

import (
	"errors"
)

var (
	ErrUnsupportedEncodeFormat = errors.New("unsupported mime type for encoder")
	ErrUnableToAddPad          = errors.New("could not add pads to bin")
)
