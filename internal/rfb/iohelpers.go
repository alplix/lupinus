package rfb

import (
	"encoding/binary"
	"io"
)

func readFull(r io.Reader, n int) ([]byte, error) {
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

func readUint8(r io.Reader) (uint8, error) {
	b, err := readFull(r, 1)
	if err != nil {
		return 0, err
	}
	return b[0], nil
}

func readUint16(r io.Reader) (uint16, error) {
	b, err := readFull(r, 2)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint16(b), nil
}

func readUint32(r io.Reader) (uint32, error) {
	b, err := readFull(r, 4)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint32(b), nil
}

func writeUint8(w io.Writer, v uint8) error {
	_, err := w.Write([]byte{v})
	return err
}

func writeUint16(w io.Writer, v uint16) error {
	var b [2]byte
	binary.BigEndian.PutUint16(b[:], v)
	_, err := w.Write(b[:])
	return err
}

func writeUint32(w io.Writer, v uint32) error {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], v)
	_, err := w.Write(b[:])
	return err
}
