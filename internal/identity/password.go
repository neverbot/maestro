// Package identity owns users, sessions, invites and API tokens.
package identity

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"

	"github.com/neverbot/maestro/internal/config"
)

// maxDecodedMemoryKiB bounds the memory cost accepted from an encoded hash.
// argon2.IDKey allocates memory proportional to this value, so without a
// ceiling a corrupted or maliciously crafted hash string could make
// VerifyPassword allocate an unbounded amount of memory before any password
// comparison happens. 4 GiB is far above any sane production setting.
const maxDecodedMemoryKiB = 4 * 1024 * 1024

// HashPassword returns an encoded argon2id hash, salt included.
func HashPassword(password string, p config.Argon2Params) (string, error) {
	salt := make([]byte, p.SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("read salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, p.Time, p.Memory, p.Threads, p.KeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.Memory, p.Time, p.Threads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

// VerifyPassword compares a password against an encoded hash in constant time.
//
// The encoded hash is untrusted input as far as this function is concerned
// (today it comes from the database, but nothing stops a future caller from
// feeding it something else), so every field is validated before it is used:
// argon2.IDKey panics if time or threads is zero, and would otherwise happily
// allocate gigabytes of memory for an attacker-chosen "m=" value. All of that
// runs before any password comparison, so it must fail closed on bad input
// rather than crash the process or become a resource-exhaustion vector.
func VerifyPassword(password, encoded string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return false, fmt.Errorf("malformed argon2id hash")
	}

	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return false, fmt.Errorf("malformed version: %w", err)
	}
	if version != argon2.Version {
		return false, fmt.Errorf("unsupported argon2 version %d", version)
	}

	var memory, time uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &time, &threads); err != nil {
		return false, fmt.Errorf("malformed parameters: %w", err)
	}
	// argon2.IDKey panics if time or threads is less than 1, and allocates
	// memory proportional to the (otherwise unbounded) memory parameter.
	if time < 1 {
		return false, fmt.Errorf("malformed parameters: time must be at least 1")
	}
	if threads < 1 {
		return false, fmt.Errorf("malformed parameters: threads must be at least 1")
	}
	if memory > maxDecodedMemoryKiB {
		return false, fmt.Errorf("malformed parameters: memory %d KiB exceeds the maximum of %d KiB", memory, maxDecodedMemoryKiB)
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, fmt.Errorf("malformed salt: %w", err)
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, fmt.Errorf("malformed key: %w", err)
	}
	if len(want) == 0 {
		return false, fmt.Errorf("malformed key: empty")
	}

	got := argon2.IDKey([]byte(password), salt, time, memory, threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}
