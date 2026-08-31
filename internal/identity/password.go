// Package identity owns users, sessions, invites and API tokens.
package identity

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"

	"github.com/neverbot/maestro/internal/config"
)

// Bounds on the fields decoded from an untrusted encoded hash string.
//
// The upper bounds guard availability: argon2.IDKey allocates memory
// proportional to "m=", and the decoded key length is passed straight
// through as its keyLen, so an oversized "m=", salt or key would force a
// correspondingly large allocation before any password comparison happens.
//
// The lower bounds guard authentication, which is a different and more
// serious failure: a key or salt shorter than these makes the comparison
// narrow enough to pass by chance instead of by design. An empty key is the
// extreme case and a total bypass (see the comment above the length check
// below); a short-but-nonempty key is the same failure partially — a
// 1-byte key makes a wrong password verify roughly 1 time in 256. This can
// happen without any attacker at all: a truncating column type, a botched
// migration, a partial write.
const (
	maxDecodedMemoryKiB = 1024 * 1024 // production cost is 64 MiB; a sanity ceiling, not a tuning knob
	minDecodedSaltLen   = 8           // the argon2 spec's minimum salt length
	maxDecodedSaltLen   = 256         // production salt is 16 bytes
	minDecodedKeyLen    = 16          // half the production key length; still enough to make a chance match implausible
	maxDecodedKeyLen    = 256         // production key is 32 bytes
)

// HashPassword returns an encoded argon2id hash, salt included.
func HashPassword(password string, p config.Argon2Params) (string, error) {
	// argon2.IDKey panics if Time or Threads is zero, and panics with a nil
	// pointer dereference if KeyLen is zero; SaltLen zero doesn't panic at
	// all, it silently produces a hash with an empty salt that can never be
	// verified again. These parameters are hard-coded by config.Load today,
	// but they exist on Config so they can become tunable, so this function
	// cannot assume they're already valid.
	if p.Time < 1 {
		return "", fmt.Errorf("argon2 params: time must be at least 1")
	}
	if p.Threads < 1 {
		return "", fmt.Errorf("argon2 params: threads must be at least 1")
	}
	if p.KeyLen < 1 {
		return "", fmt.Errorf("argon2 params: key length must be at least 1")
	}
	if p.SaltLen < 1 {
		return "", fmt.Errorf("argon2 params: salt length must be at least 1")
	}

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

// decodedHash is an encoded argon2id hash string, parsed and validated.
type decodedHash struct {
	memory   uint32
	timeCost uint32
	threads  uint8
	salt     []byte
	key      []byte
}

// parseEncodedHash parses and validates an encoded argon2id hash string.
//
// The string is untrusted input as far as this function is concerned
// (today it comes from the database, but nothing stops a future caller from
// feeding it something else), so every field is checked before it is used
// anywhere else: argon2.IDKey panics if time or threads is zero, and the
// bounds in the const block above guard against both an oversized field
// (availability) and an undersized one (authentication). The parser itself
// is total — it uses strings.CutPrefix and strconv.ParseUint instead of
// fmt.Sscanf, which silently accepts trailing garbage ("t=1XXXX") and
// leading whitespace ("m= 8192") because it only requires its format string
// to match a prefix of the input, not the whole thing.
func parseEncodedHash(encoded string) (decodedHash, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return decodedHash{}, fmt.Errorf("malformed argon2id hash")
	}

	verStr, ok := strings.CutPrefix(parts[2], "v=")
	if !ok {
		return decodedHash{}, fmt.Errorf("malformed version")
	}
	version, err := strconv.ParseUint(verStr, 10, 32)
	if err != nil {
		return decodedHash{}, fmt.Errorf("malformed version: %w", err)
	}
	if uint32(version) != argon2.Version {
		return decodedHash{}, fmt.Errorf("unsupported argon2 version %d", version)
	}

	params := strings.Split(parts[3], ",")
	if len(params) != 3 {
		return decodedHash{}, fmt.Errorf("malformed parameters")
	}
	mStr, ok := strings.CutPrefix(params[0], "m=")
	if !ok {
		return decodedHash{}, fmt.Errorf("malformed parameters: missing m=")
	}
	tStr, ok := strings.CutPrefix(params[1], "t=")
	if !ok {
		return decodedHash{}, fmt.Errorf("malformed parameters: missing t=")
	}
	pStr, ok := strings.CutPrefix(params[2], "p=")
	if !ok {
		return decodedHash{}, fmt.Errorf("malformed parameters: missing p=")
	}
	memory, err := strconv.ParseUint(mStr, 10, 32)
	if err != nil {
		return decodedHash{}, fmt.Errorf("malformed memory parameter: %w", err)
	}
	timeCost, err := strconv.ParseUint(tStr, 10, 32)
	if err != nil {
		return decodedHash{}, fmt.Errorf("malformed time parameter: %w", err)
	}
	threads, err := strconv.ParseUint(pStr, 10, 8)
	if err != nil {
		return decodedHash{}, fmt.Errorf("malformed threads parameter: %w", err)
	}
	// argon2.IDKey panics if time or threads is less than 1, and allocates
	// memory proportional to the (otherwise unbounded) memory parameter.
	if timeCost < 1 {
		return decodedHash{}, fmt.Errorf("malformed parameters: time must be at least 1")
	}
	if threads < 1 {
		return decodedHash{}, fmt.Errorf("malformed parameters: threads must be at least 1")
	}
	if memory > maxDecodedMemoryKiB {
		return decodedHash{}, fmt.Errorf("malformed parameters: memory %d KiB exceeds the maximum of %d KiB", memory, maxDecodedMemoryKiB)
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return decodedHash{}, fmt.Errorf("malformed salt: %w", err)
	}
	if len(salt) < minDecodedSaltLen {
		return decodedHash{}, fmt.Errorf("malformed salt: %d bytes is below the minimum of %d", len(salt), minDecodedSaltLen)
	}
	if len(salt) > maxDecodedSaltLen {
		return decodedHash{}, fmt.Errorf("malformed salt: %d bytes exceeds the maximum of %d", len(salt), maxDecodedSaltLen)
	}

	key, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return decodedHash{}, fmt.Errorf("malformed key: %w", err)
	}
	// A key shorter than minDecodedKeyLen is not just malformed input: it
	// narrows the comparison enough for a wrong password to match by
	// chance. An empty key is the extreme case: argon2.IDKey with keyLen=0
	// returns a zero-length slice, and subtle.ConstantTimeCompare reports
	// two zero-length slices as equal, so every password would verify
	// against a hash string ending in "$<salt>$". This is an authentication
	// bypass, not merely a robustness issue.
	if len(key) < minDecodedKeyLen {
		return decodedHash{}, fmt.Errorf("malformed key: %d bytes is below the minimum of %d", len(key), minDecodedKeyLen)
	}
	if len(key) > maxDecodedKeyLen {
		return decodedHash{}, fmt.Errorf("malformed key: %d bytes exceeds the maximum of %d", len(key), maxDecodedKeyLen)
	}

	return decodedHash{
		memory:   uint32(memory),
		timeCost: uint32(timeCost),
		threads:  uint8(threads),
		salt:     salt,
		key:      key,
	}, nil
}

// VerifyPassword compares a password against an encoded hash in constant time.
//
// See parseEncodedHash for how the encoded hash is validated before any of
// it is used.
func VerifyPassword(password, encoded string) (bool, error) {
	h, err := parseEncodedHash(encoded)
	if err != nil {
		return false, err
	}

	got := argon2.IDKey([]byte(password), h.salt, h.timeCost, h.memory, h.threads, uint32(len(h.key)))
	// subtle.ConstantTimeCompare, not bytes.Equal: bytes.Equal returns as
	// soon as it finds a differing byte, so its running time leaks how many
	// leading bytes of the derived key matched the stored one. Nothing in
	// this package's test suite would fail if this were swapped for
	// bytes.Equal, and a timing assertion in a unit test would be flaky and
	// eventually get deleted, so this comment is the guard: do not
	// "simplify" this back to bytes.Equal.
	return subtle.ConstantTimeCompare(got, h.key) == 1, nil
}

// NeedsRehash reports whether encoded was produced with different argon2
// cost parameters than p. VerifyPassword already parses out the cost
// parameters and discards them; this exposes that answer instead of making
// every caller duplicate the parser. A login handler calls this after a
// successful VerifyPassword and, if it returns true, re-hashes the password
// with the current parameters and stores the new hash — this is what lets
// the production Argon2 cost be raised later without invalidating existing
// passwords.
func NeedsRehash(encoded string, p config.Argon2Params) (bool, error) {
	h, err := parseEncodedHash(encoded)
	if err != nil {
		return false, err
	}
	return h.memory != p.Memory ||
		h.timeCost != p.Time ||
		h.threads != p.Threads ||
		uint32(len(h.key)) != p.KeyLen, nil
}
