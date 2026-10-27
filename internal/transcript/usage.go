package transcript

import (
	"bufio"
	"io"
	"os"

	"google.golang.org/protobuf/proto"

	"github.com/ironpark/mako/internal/agent"
	"github.com/ironpark/mako/internal/protocol"
)

// usageScan adds up the tokens a log records as it grows: each read goes on
// from where the last stopped, so a long conversation is read once.
type usageScan struct {
	path   string
	offset int64
	count  agent.Usage // the agent's counter, which keeps what it has seen
	usage  *protocol.Usage
}

// read brings the scan up to the end of path's complete records, and
// returns a copy of the usage, nil while nothing was counted.
func (u *usageScan) read(path string, a agent.Logs) *protocol.Usage {
	f, err := os.Open(path)
	if err != nil {
		return u.result()
	}
	defer f.Close()
	if stat, err := f.Stat(); err != nil || u.count == nil || u.path != path || stat.Size() < u.offset {
		*u = usageScan{path: path, count: a.NewUsage(), usage: &protocol.Usage{}} // another log, or this one rewritten
	}
	if _, err := f.Seek(u.offset, io.SeekStart); err != nil {
		return u.result()
	}
	reader := bufio.NewReaderSize(f, 64<<10)
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			break // the writer may be in the middle of a record
		}
		u.offset += int64(len(line))
		u.count.Count(line, u.usage)
	}
	return u.result()
}

// result is a copy of the usage counted, for the caller to keep; nil for
// none.
func (u *usageScan) result() *protocol.Usage {
	if u.usage.IsZero() {
		return nil
	}
	return proto.CloneOf(u.usage)
}
