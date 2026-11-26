package claude

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/ironpark/mako/pkg/fsutil"
)

type rewindRecord struct {
	Type       string `json:"type"`
	UUID       string `json:"uuid"`
	ParentUUID string `json:"parentUuid"`
	Sidechain  bool   `json:"isSidechain"`
	Attachment *struct {
		Type       string `json:"type"`
		SourceUUID string `json:"source_uuid"`
	} `json:"attachment"`
}

// ForkTranscriptBefore non-destructively copies Claude JSONL records strictly
// before the target user turn into a new UUID-named transcript file in the same
// project folder, rewriting sessionId to the new UUID.
//
// targetUUID is matched against record uuid (or a queued_command attachment's
// source_uuid for steered messages, in which case the fork keeps records up to
// and including the attachment's parentUuid). When targetUUID is empty or not
// found, userTurnIndex (0-based visible user message index) is used.
//
// If the cut falls before any prior user/assistant turn exists, zeroTurns is
// true and no file is written so the caller can open a fresh session instead.
func ForkTranscriptBefore(srcPath, targetUUID string, userTurnIndex int) (newSessionID, newPath string, zeroTurns bool, err error) {
	if srcPath == "" {
		return "", "", false, errors.New("claude rewind: missing transcript path")
	}
	f, err := os.Open(srcPath)
	if err != nil {
		return "", "", false, fmt.Errorf("claude rewind: open transcript: %w", err)
	}
	defer f.Close()

	var lines [][]byte
	br := bufio.NewReaderSize(f, 64<<10)
	for {
		line, rerr := br.ReadBytes('\n')
		if len(line) > 0 {
			trimmed := bytes.TrimRight(line, "\r\n")
			if len(trimmed) > 0 {
				lines = append(lines, trimmed)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return "", "", false, fmt.Errorf("claude rewind: read transcript: %w", rerr)
		}
	}

	cutIdx := findClaudeCutIndex(lines, strings.TrimSpace(targetUUID), userTurnIndex)
	if cutIdx < 0 {
		return "", "", false, errors.New("claude rewind: target user message not found in transcript")
	}

	prefix := lines[:cutIdx]
	hasPriorTurn := false
	for _, line := range prefix {
		var rec rewindRecord
		if json.Unmarshal(line, &rec) == nil && !rec.Sidechain && (rec.Type == "user" || rec.Type == "assistant") {
			hasPriorTurn = true
			break
		}
	}
	if !hasPriorTurn {
		return "", "", true, nil
	}

	newID := newUUIDv4()
	quotedID, _ := json.Marshal(newID)
	var out bytes.Buffer
	for _, line := range prefix {
		rewritten := rewriteSessionID(line, quotedID)
		out.Write(rewritten)
		out.WriteByte('\n')
	}

	dstPath := filepath.Join(filepath.Dir(srcPath), newID+".jsonl")
	if err := fsutil.Write(dstPath, out.Bytes()); err != nil {
		return "", "", false, fmt.Errorf("claude rewind: write forked transcript: %w", err)
	}
	return newID, dstPath, false, nil
}

func findClaudeCutIndex(lines [][]byte, targetUUID string, userTurnIndex int) int {
	if targetUUID != "" {
		// 1. Check for a steered message recorded as a queued_command attachment.
		// Keep up to and including its parentUuid record, dropping the steered turn and after.
		for _, line := range lines {
			if !bytes.Contains(line, []byte(targetUUID)) || !bytes.Contains(line, []byte(`"queued_command"`)) {
				continue
			}
			var rec rewindRecord
			if json.Unmarshal(line, &rec) != nil {
				continue
			}
			if rec.Type == "attachment" && rec.Attachment != nil &&
				rec.Attachment.Type == "queued_command" &&
				rec.Attachment.SourceUUID == targetUUID &&
				rec.ParentUUID != "" {
				for j, pline := range lines {
					var prec rewindRecord
					if json.Unmarshal(pline, &prec) == nil && prec.UUID == rec.ParentUUID {
						return j + 1
					}
				}
			}
		}
		// 2. Direct record UUID match: cut strictly before this record.
		for i, line := range lines {
			if !bytes.Contains(line, []byte(targetUUID)) {
				continue
			}
			var rec rewindRecord
			if json.Unmarshal(line, &rec) == nil && !rec.Sidechain && rec.UUID == targetUUID {
				return i
			}
		}
	}

	// 3. Fallback: match by 0-based visible user message index using the same
	// decoder Mako's chat view uses.
	if userTurnIndex >= 0 {
		dec := New().NewDecoder()
		seenUsers := 0
		for i, line := range lines {
			msgs := dec.Line(line)
			for _, m := range msgs {
				if m.GetRole() == "user" {
					if seenUsers == userTurnIndex {
						return i
					}
					seenUsers++
				}
			}
		}
	}
	return -1
}

func rewriteSessionID(line []byte, quotedNewID []byte) []byte {
	if !bytes.Contains(line, []byte(`"sessionId"`)) {
		return line
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(line, &obj) != nil {
		return line
	}
	if _, ok := obj["sessionId"]; !ok {
		return line
	}
	obj["sessionId"] = json.RawMessage(quotedNewID)
	b, err := json.Marshal(obj)
	if err != nil {
		return line
	}
	return b
}

func newUUIDv4() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
