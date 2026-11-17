package workspace

import (
	"crypto/sha256"

	"google.golang.org/protobuf/proto"

	"github.com/ironpark/mako/internal/protocol"
)

// Digest is a hash of st, for a node to tell whether a peer's state differs
// from its own without being sent it: equal states have equal digests, and
// an empty state, or none, has the same digest always. It hashes st's
// deterministic encoding, whose map entries are sorted, but which another
// build of the protobuf library may lay out otherwise; so a digest is only
// a hint, and two that differ mean only that the state is sent once more.
func Digest(st *protocol.WorkspaceState) []byte {
	data, err := proto.MarshalOptions{Deterministic: true}.Marshal(st)
	if err != nil {
		// A state that does not encode cannot be sent either; its digest
		// says nothing but that it is none of those that do.
		data = []byte(err.Error())
	}
	sum := sha256.Sum256(data)
	return sum[:]
}
