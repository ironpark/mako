package agent

import (
	"cmp"
	"encoding/json"
	"io"
	"os"
	"slices"
	"time"

	"github.com/ironpark/mako/internal/protocol"
)

// StaleAgent is how long a subagent can go without writing before it is
// taken to have stopped, as when its session was killed under it.
const StaleAgent = 15 * time.Minute

// Tail reads a log as it grows, a complete record at a time, on from where
// the last read stopped, and keeps when its records say they were written.
type Tail struct {
	offset           int64
	file             os.FileInfo
	Started, Updated time.Time
	stamp            struct { // a record's time, decoded in place not to allocate it
		Timestamp time.Time `json:"timestamp"`
	}
}

// Read reads path's records written since the last read, handing each to
// line. A replaced, truncated or rewritten log is read again from its
// start, after reset drops what the caller kept of it.
//
// The record line is given is valid only until line returns: it lies in a
// buffer the next record is read into, which other reads use after, so
// line copies what it keeps of it.
func (t *Tail) Read(path string, reset func(), line func([]byte)) {
	stat, err := os.Stat(path)
	if err != nil {
		return
	}
	if stat.Size() < t.offset || (t.file != nil && (!os.SameFile(t.file, stat) || (t.file.Size() == stat.Size() && !t.file.ModTime().Equal(stat.ModTime())))) {
		*t = Tail{}
		reset()
	}
	t.file = stat
	if stat.Size() == t.offset {
		return
	}
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	if _, err := f.Seek(t.offset, io.SeekStart); err != nil {
		return
	}
	reader := BorrowReader(f)
	var long []byte // a record longer than reader's buffer, put together
	defer func() { GiveBack(reader, long) }()
	for {
		l, err := ReadRecord(reader, &long)
		if err != nil {
			return // the writer may be in the middle of a record
		}
		t.offset += int64(len(l))
		t.stamp.Timestamp = time.Time{}
		if json.Unmarshal(l, &t.stamp) == nil && !t.stamp.Timestamp.IsZero() {
			t.Started = cmp.Or(t.Started, t.stamp.Timestamp)
			t.Updated = t.stamp.Timestamp
		}
		line(l)
	}
}

// SortAgents puts subagents in the order they started.
func SortAgents(agents []*protocol.SubAgent) {
	slices.SortStableFunc(agents, func(a, b *protocol.SubAgent) int { return cmp.Compare(a.GetStarted(), b.GetStarted()) })
}
