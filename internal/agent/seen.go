package agent

import "hash/maphash"

// Seen is the IDs a log's reader has read. A long conversation's are tens
// of thousands, read for as long as it is followed, so each is kept as two
// independent 64-bit hashes rather than as its string: a sixth of the room,
// and nothing for the collector to scan.
type Seen struct {
	seeds [2]maphash.Seed
	set   map[[2]uint64]struct{}
}

// Add records id, reporting whether it was new.
func (s *Seen) Add(id string) bool {
	if s.set == nil {
		s.seeds = [2]maphash.Seed{maphash.MakeSeed(), maphash.MakeSeed()}
		s.set = map[[2]uint64]struct{}{}
	}
	key := [2]uint64{maphash.String(s.seeds[0], id), maphash.String(s.seeds[1], id)}
	if _, ok := s.set[key]; ok {
		return false
	}
	s.set[key] = struct{}{}
	return true
}
