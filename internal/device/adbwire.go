package device

import (
	"fmt"
	"io"
	"strconv"

	"google.golang.org/protobuf/encoding/protowire"
)

// adb's own protocol between a client and its server: a request is its
// length, as four hex digits, then itself; the server answers OKAY, or
// FAIL and a message, and a message is framed as a request is.

// maxMessage is the longest message four hex digits frame.
const maxMessage = 0xffff

func readMessage(r io.Reader) (string, error) {
	var head [4]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return "", err
	}
	n, err := strconv.ParseUint(string(head[:]), 16, 16)
	if err != nil {
		return "", fmt.Errorf("adb: %q is not a length", head[:])
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return "", err
	}
	return string(buf), nil
}

func writeMessage(w io.Writer, msg string) error {
	if len(msg) > maxMessage {
		return fmt.Errorf("adb: a message of %d bytes is too long", len(msg))
	}
	_, err := fmt.Fprintf(w, "%04x%s", len(msg), msg)
	return err
}

// failure is the message of a server's FAIL.
type failure string

func (f failure) Error() string { return string(f) }

// readStatus reads OKAY, or FAIL and its message, as a failure.
func readStatus(r io.Reader) error {
	var status [4]byte
	if _, err := io.ReadFull(r, status[:]); err != nil {
		return err
	}
	switch string(status[:]) {
	case "OKAY":
		return nil
	case "FAIL":
		msg, err := readMessage(r)
		if err != nil {
			return err
		}
		return failure(msg)
	}
	return fmt.Errorf("adb: %q is not a status", status[:])
}

func writeFail(w io.Writer, msg string) error {
	if len(msg) > maxMessage {
		msg = msg[:maxMessage]
	}
	if _, err := io.WriteString(w, "FAIL"); err != nil {
		return err
	}
	return writeMessage(w, msg)
}

// keepProto keeps serial's of the devices in adb's binary listing, a
// Devices message of adb_host.proto: Device device = 1, whose serial is
// its field 1. A listing that does not parse keeps none.
func keepProto(serial string) func(string) string {
	return func(msg string) string {
		b := []byte(msg)
		var out []byte
		for len(b) > 0 {
			num, typ, n := protowire.ConsumeTag(b)
			if n < 0 {
				return ""
			}
			m := protowire.ConsumeFieldValue(num, typ, b[n:])
			if m < 0 {
				return ""
			}
			field := b[:n+m]
			b = b[n+m:]
			if num == 1 && typ == protowire.BytesType {
				dev, _ := protowire.ConsumeBytes(field[n:])
				if protoSerial(dev) != serial {
					continue
				}
			}
			out = append(out, field...)
		}
		return string(out)
	}
}

// protoSerial is a Device message's serial, its field 1.
func protoSerial(dev []byte) string {
	for len(dev) > 0 {
		num, typ, n := protowire.ConsumeTag(dev)
		if n < 0 {
			return ""
		}
		if num == 1 && typ == protowire.BytesType {
			v, m := protowire.ConsumeBytes(dev[n:])
			if m < 0 {
				return ""
			}
			return string(v)
		}
		m := protowire.ConsumeFieldValue(num, typ, dev[n:])
		if m < 0 {
			return ""
		}
		dev = dev[n+m:]
	}
	return ""
}
