package domain

import (
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// User is one participant in the demo.
//
// There is no password and no session: a demo where two people are two browser
// tabs has nothing to protect, and pretending otherwise would put an
// authentication story in the way of the one being told.
type User struct {
	ID   uuid.UUID
	Name string

	// PublicKey is the wallet's, compressed. It is also the key the contract's
	// leaves carry, because in this demo the service holds the wallet.
	//
	// When wallets move out to the user's own device the two come apart: the
	// contract key stays a key the user proves control of, and the service
	// stops having either.
	PublicKey []byte
}

const maxUserName = 32

// ValidateName is the whole of what a demo user needs to be.
func ValidateName(name string) (string, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "", fmt.Errorf("a name is required")
	}
	if len(trimmed) > maxUserName {
		return "", fmt.Errorf("a name is at most %d characters, got %d", maxUserName, len(trimmed))
	}
	return trimmed, nil
}
