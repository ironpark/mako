package crdt

// Register is a last-writer-wins register: one value, and the stamp of the
// edit that wrote it. Each field of an entity is a register of its own, so
// two nodes that edit different fields of one entity both keep their edit.
type Register[T any] struct {
	Value T
	Clock HLC
}

// Merge takes other's value if other was written later, in the order of
// key, the field both are, and reports whether it did.
func (r *Register[T]) Merge(other Register[T], key string) (changed bool) {
	if other.Clock.After(r.Clock, key) {
		*r = other
		return true
	}
	return false
}

// MergeBy merges other into r as Merge does, and when both carry the same
// stamp but different values, takes the greater value by compare. One
// stamp is one edit, so the tie arises only from a node that lost its
// state and counted again from the start; settling it either way would
// do, but settling it the same way everywhere keeps the nodes agreeing.
func MergeBy[T any](r *Register[T], other Register[T], key string, compare func(a, b T) int) (changed bool) {
	if r.Merge(other, key) {
		return true
	}
	if other.Clock == r.Clock && compare(other.Value, r.Value) > 0 {
		r.Value = other.Value
		return true
	}
	return false
}
