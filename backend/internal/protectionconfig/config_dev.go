//go:build !protected

package protectionconfig

import (
	"crypto/ed25519"
	"errors"
)

func Enabled() bool { return false }

func BuildID() string { return "" }

func Credentials() ([]byte, ed25519.PublicKey, error) {
	return nil, nil, errors.New("this is a development build without an encrypted plugin bundle")
}
