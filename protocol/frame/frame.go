// Package frame supplies optional framing for protobuf messages over a stream.
// DLL calls and NetworkExtension provider messages already have message bounds
// and must use raw protobuf without this header. This package does not provide
// peer authentication; a privileged host must authenticate its transport.
package frame

import (
	"encoding/binary"
	"fmt"
	"io"
)

type Kind uint16

const (
	Request  Kind = 1
	Response Kind = 2
)
const HeaderSize = 12
const MaxMessageBytes = 4 << 20

var magic = [4]byte{'L', 'B', 'X', 'P'}

func Write(w io.Writer, kind Kind, payload []byte) error {
	if (kind != Request && kind != Response) || len(payload) == 0 || len(payload) > MaxMessageBytes {
		return fmt.Errorf("invalid control frame")
	}
	var header [HeaderSize]byte
	copy(header[:4], magic[:])
	binary.LittleEndian.PutUint16(header[4:6], 1)
	binary.LittleEndian.PutUint16(header[6:8], uint16(kind))
	binary.LittleEndian.PutUint32(header[8:12], uint32(len(payload)))
	for _, buffer := range [][]byte{header[:], payload} {
		for len(buffer) > 0 {
			n, err := w.Write(buffer)
			if n < 0 || n > len(buffer) {
				return io.ErrShortWrite
			}
			buffer = buffer[n:]
			if err != nil {
				return err
			}
			if n == 0 {
				return io.ErrShortWrite
			}
		}
	}
	return nil
}
func Read(r io.Reader, kind Kind) ([]byte, error) {
	var header [HeaderSize]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, err
	}
	length := binary.LittleEndian.Uint32(header[8:12])
	if (kind != Request && kind != Response) || [4]byte(header[:4]) != magic || binary.LittleEndian.Uint16(header[4:6]) != 1 || Kind(binary.LittleEndian.Uint16(header[6:8])) != kind || length == 0 || length > MaxMessageBytes {
		return nil, fmt.Errorf("invalid control frame header")
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, err
	}
	return payload, nil
}
