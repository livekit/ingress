package utils

import (
	"encoding/binary"
	"io"
	"time"
)

/*
  This package provides utilities to serialize and deserialize whip media packets
  over the service -> handler relay.

  The format is

  |----------------------------------------------------------------|
  | 0------------63 | 0------------32 | 0 ------------- media size |
  | timestamp (BE)  | media size (BE) |        media payload       |
  |----------------------------------------------------------------|

*/

func SerializeMediaForRelay(w io.Writer, data []byte, ts time.Duration) error {
	err := binary.Write(w, binary.BigEndian, ts)
	if err != nil {
		return err
	}

	err = binary.Write(w, binary.BigEndian, uint32(len(data)))
	if err != nil {
		return err
	}

	err = binary.Write(w, binary.BigEndian, data)
	if err != nil {
		return err
	}

	return nil
}

func DeserializeMediaForRelay(r io.Reader) ([]byte, time.Duration, error) {
	var ts time.Duration

	err := binary.Read(r, binary.BigEndian, &ts)
	if err != nil {
		return nil, 0, err
	}

	var size uint32
	err = binary.Read(r, binary.BigEndian, &size)
	if err != nil {
		return nil, 0, err
	}

	data := make([]byte, int(size))
	err = binary.Read(r, binary.BigEndian, data)
	if err != nil {
		return nil, 0, err
	}

	return data, ts, nil
}
