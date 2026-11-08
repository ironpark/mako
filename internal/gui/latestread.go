package gui

// latestRead keeps a loader to one read under way at a time. One asked for
// meanwhile, of something else, is read once that one is done: the latest
// asked for only, as a project picked and then another needs only the
// other's. One asked for again of what is being read waits for nothing.
// UI goroutine only.
type latestRead[K comparable] struct {
	reading   bool
	cur, want K
	wanted    bool
}

// ask reports whether a read of k starts now; if another is under way,
// k is read after it, unless it is what that one reads.
func (r *latestRead[K]) ask(k K) bool {
	if !r.reading {
		r.reading, r.cur = true, k
		return true
	}
	r.want, r.wanted = k, k != r.cur
	return false
}

// done ends the read under way, and is what was asked for meanwhile, if
// anything, to read next.
func (r *latestRead[K]) done() (next K, again bool) {
	next, again = r.want, r.wanted
	*r = latestRead[K]{}
	return next, again
}
