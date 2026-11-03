package frame

import (
	"bytes"
	"encoding/binary"
	"io"
	"testing"
)

type partialWriter struct{ bytes.Buffer }

func (p *partialWriter) Write(data []byte) (int, error) {
	if len(data) > 2 {
		data = data[:2]
	}
	return p.Buffer.Write(data)
}
func TestShortWritesAndBounds(t *testing.T) {
	var output partialWriter
	if err := Write(&output, Request, []byte{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	result, err := Read(bytes.NewReader(output.Bytes()), Request)
	if err != nil || !bytes.Equal(result, []byte{1, 2, 3}) {
		t.Fatalf("%x %v", result, err)
	}
	if _, err = Read(bytes.NewReader(output.Bytes()), Response); err == nil {
		t.Fatal("accepted wrong frame kind")
	}
	corrupt := append([]byte{}, output.Bytes()...)
	binary.LittleEndian.PutUint32(corrupt[8:12], MaxMessageBytes+1)
	if _, err = Read(bytes.NewReader(corrupt[:12]), Request); err == nil {
		t.Fatal("oversized length reached allocation")
	}
	if _, err = Read(bytes.NewReader(output.Bytes()[:13]), Request); err != io.ErrUnexpectedEOF {
		t.Fatalf("wrong truncation error: %v", err)
	}
}
