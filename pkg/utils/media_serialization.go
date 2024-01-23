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

package utils

import (
	"encoding/binary"
	"io"
)

/*
  This package provides utilities to serialize and deserialize rtp packets
  over the service -> handler relay.

  The format is

  -----------------------------------------------|
  | 0------------32   | 0 -----------   rtp size |
  | rtp pkt size (BE) |        rtp packet        |
  -----------------------------------------------|

*/

func SerializeMediaForRelay(w io.Writer, data []byte) error {
	err := binary.Write(w, binary.BigEndian, uint32(len(data)))
	if err != nil {
		return err
	}

	return binary.Write(w, binary.BigEndian, data)
}

func DeserializeMediaForRelay(r io.Reader) ([]byte, error) {
	var size uint32
	err := binary.Read(r, binary.BigEndian, &size)
	if err != nil {
		return nil, err
	}

	data := make([]byte, int(size))
	err = binary.Read(r, binary.BigEndian, data)
	if err != nil {
		return nil, err
	}

	return data, nil
}
