package protectionconfig

import (
	"crypto/ed25519"
	"errors"
)

// combineCredentials reconstructs the per-build AES key from two random XOR
// shares. Generated protected builds store the shares as separate byte tables;
// Garble renames the symbols and control flow. This raises extraction cost but
// does not pretend that a key used on a client machine can be mathematically
// hidden from a determined memory analyst.
func combineCredentials(partA, partB, public []byte) ([]byte, ed25519.PublicKey, error) {
	if len(partA) != 32 || len(partB) != 32 {
		return nil, nil, errors.New("invalid protected build key shares")
	}
	if len(public) != ed25519.PublicKeySize {
		return nil, nil, errors.New("invalid protected build verification key")
	}
	key := make([]byte, 32)
	for i := range key {
		key[i] = partA[i] ^ partB[i]
	}
	pub := append(ed25519.PublicKey(nil), public...)
	return key, pub, nil
}
