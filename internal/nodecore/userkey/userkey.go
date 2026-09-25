// Package userkey is the identity the forked inbounds hand to their wire
// services in place of upstream sing-box's "index into the inbound's user
// slice".
//
// An index is only meaningful against the user list it was resolved from, and
// a handshake can take arbitrarily long (it waits on the client's next
// packet) - so if UpdateUsers swaps the list in between, the connection would
// index the new list: out of range (a panic that takes the whole node down)
// or, when the list merely reordered, bytes billed to the wrong user. A Key
// carries the name itself, so there is no second lookup to race.
package userkey

import "strconv"

// Key identifies one user of one UpdateUsers generation. Every generation
// mints fresh Keys, so a Key is never equal to one from another generation.
type Key struct {
	// Name is what sing-box calls metadata.User: empty for an unnamed user.
	Name string
	// Label is what logs and traffic accounting call the user: Name, or the
	// user's position in its list when it has no name.
	Label string
}

// String exists because sing's services format the key into their own error
// messages (trojan does, on a duplicate password) through a formatter that
// panics on a type it does not know.
func (k *Key) String() string { return k.Label }

// Build returns one fresh Key per name, in order.
func Build(names []string) []*Key {
	keys := make([]*Key, len(names))
	for i, name := range names {
		label := name
		if label == "" {
			label = strconv.Itoa(i)
		}
		keys[i] = &Key{Name: name, Label: label}
	}
	return keys
}
