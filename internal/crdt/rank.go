package crdt

import (
	"math/bits"
	"strings"
)

// Ranks order a list without indices: each item carries a rank, a string
// read as a fraction between 0 and 1 in base 62 ("V" is about one half),
// and the list is its items sorted by rank, then by ID. Moving an item
// rewrites its rank alone, to one between its new neighbours', so two
// nodes moving or adding different items do not disturb each other.
//
// A rank never ends in the least digit, "0": "a" and "a0" would be the
// same fraction but different strings, with nothing between them.

// digits are the ranks' digits, in the order bytes compare in, so that
// comparing ranks as strings compares them as fractions.
const digits = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

const base = len(digits)

// maxInitial bounds Initial's n, so that the space it divides fits 64 bits.
const maxInitial = 839299365868340224 // 62^10

// digitOf is c's value as a digit; a byte that is none counts as the first
// digit above it, or the last.
func digitOf(c byte) int {
	for i := range base {
		if digits[i] >= c {
			return i
		}
	}
	return base - 1
}

// Between is a rank after prev and before next, "" for either standing for
// the list's end, so Between("", "") is a first rank. prev must come
// before next; given ranks the other way round, or ones this package did
// not make, it still answers, with a rank after prev.
func Between(prev, next string) string {
	prev = strings.TrimRight(prev, "0")
	if trimmed := strings.TrimRight(next, "0"); trimmed == "" || prev >= trimmed {
		next = ""
	} else {
		next = trimmed
	}
	return midpoint(prev, next)
}

// midpoint is a rank between a and b, b "" for 1, neither ending in "0".
func midpoint(a, b string) string {
	if b != "" {
		// The digits both share, a read with as many zeros after it as it
		// takes, stay; the rank is made after them.
		n := 0
		for n < len(b) && at(a, n) == b[n] {
			n++
		}
		if n > 0 {
			return b[:n] + midpoint(from(a, n), b[n:])
		}
	}
	lo, hi := 0, base
	if a != "" {
		lo = digitOf(a[0])
	}
	if b != "" {
		hi = digitOf(b[0])
	}
	if hi-lo > 1 {
		return string(digits[(lo+hi)/2])
	}
	// The first digits are neighbours. b's first digit alone is less than
	// b when b goes on; otherwise the rank follows a's first digit and
	// goes after the rest of a.
	if len(b) > 1 {
		return b[:1]
	}
	return string(digits[lo]) + midpoint(from(a, 1), "")
}

// at is the digit of s at i, "0" past its end.
func at(s string, i int) byte {
	if i < len(s) {
		return s[i]
	}
	return digits[0]
}

// from is s from i on, "" past its end.
func from(s string, i int) string {
	if i < len(s) {
		return s[i:]
	}
	return ""
}

// Initial is the rank of the i-th of n items ranked at once, 0 <= i < n:
// the ranks spread evenly between 0 and 1, rise with i, are the shortest
// that do, and are the same wherever they are made, so that nodes ranking
// the same list each on their own agree. n is at most 62^10, far beyond
// any list a workspace holds.
func Initial(i, n int) string {
	if i < 0 || i >= n || uint64(n) >= maxInitial {
		panic("crdt: Initial out of range")
	}
	width, space := 1, uint64(base)
	for space < uint64(n)+1 {
		width, space = width+1, space*uint64(base)
	}
	hi, lo := bits.Mul64(uint64(i)+1, space)
	v, _ := bits.Div64(hi, lo, uint64(n)+1)
	b := make([]byte, width)
	for j := width - 1; j >= 0; j-- {
		b[j] = digits[v%uint64(base)]
		v /= uint64(base)
	}
	return strings.TrimRight(string(b), "0")
}

// Rerank is the ranks to give the items of order, in that order, keeping
// as many of their ranks as it can: those in ranks that already rise along
// order the most items keep, and each other item, or one without a rank
// (""), is ranked between its new neighbours. It returns only the ranks
// that change, so that a move rewrites the moved item's rank alone, and
// two items of the same rank, as two nodes inserting at one spot make,
// are set apart.
func Rerank(order []string, ranks map[string]string) map[string]string {
	keep := risingRanks(order, ranks)
	out := map[string]string{}
	prev := ""
	for i, id := range order {
		if keep[i] {
			prev = ranks[id]
			continue
		}
		next := ""
		for j := i + 1; j < len(order); j++ {
			if keep[j] {
				next = ranks[order[j]]
				break
			}
		}
		r := Between(prev, next)
		if r != ranks[id] {
			out[id] = r
		}
		prev = r
	}
	return out
}

// risingRanks marks the items of order in a longest run, not necessarily
// adjacent, whose ranks strictly rise; items without a rank are in none.
func risingRanks(order []string, ranks map[string]string) []bool {
	n := len(order)
	length, from := make([]int, n), make([]int, n)
	best := -1
	for i, id := range order {
		r := ranks[id]
		if r == "" {
			continue
		}
		length[i], from[i] = 1, -1
		for j := range i {
			if length[j] > 0 && ranks[order[j]] < r && length[j]+1 > length[i] {
				length[i], from[i] = length[j]+1, j
			}
		}
		if best < 0 || length[i] > length[best] {
			best = i
		}
	}
	keep := make([]bool, n)
	for i := best; i >= 0; i = from[i] {
		keep[i] = true
	}
	return keep
}
