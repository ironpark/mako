package agent

import (
	"bufio"
	"io"
	"sync"
)

// readers are the buffered readers reads of logs borrow, and longLines the
// room they put a long record together in. A log is read by many scans,
// one for each subagent and for each session, but by few at once: each
// holding a reader of its own would keep tens of megabytes.
var (
	readers   = sync.Pool{New: func() any { return bufio.NewReaderSize(nil, 64<<10) }}
	longLines sync.Pool
)

// maxLongLine is the room for a long record kept for the next.
const maxLongLine = 1 << 20

// BorrowReader is a buffered reader of r, to give back with GiveBack.
func BorrowReader(r io.Reader) *bufio.Reader {
	reader := readers.Get().(*bufio.Reader)
	reader.Reset(r)
	return reader
}

// GiveBack returns reader, and the room ReadRecord put long records
// together in, for other reads to use.
func GiveBack(reader *bufio.Reader, long []byte) {
	reader.Reset(nil) // not to keep the file
	readers.Put(reader)
	if cap(long) > 0 && cap(long) <= maxLongLine {
		longLines.Put(&long)
	}
}

// ReadRecord is the next line of reader, up to and with its newline: in
// reader's own buffer, valid until it reads again, when it fits, else put
// together in long, which comes from longLines when it is nil. A line
// that does not end in a newline comes with an error.
func ReadRecord(reader *bufio.Reader, long *[]byte) ([]byte, error) {
	line, err := reader.ReadSlice('\n')
	if err != bufio.ErrBufferFull {
		return line, err
	}
	if *long == nil {
		if p, ok := longLines.Get().(*[]byte); ok {
			*long = *p
		}
	}
	buf := append((*long)[:0], line...)
	for err == bufio.ErrBufferFull {
		line, err = reader.ReadSlice('\n')
		buf = append(buf, line...)
	}
	*long = buf
	return buf, err
}
