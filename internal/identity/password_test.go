package identity

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/neverbot/maestro/internal/assert"
	"github.com/neverbot/maestro/internal/config"
)

var testParams = config.Argon2Params{Time: 1, Memory: 8 * 1024, Threads: 1, KeyLen: 32, SaltLen: 16}

func TestHashAndVerify(t *testing.T) {
	t.Parallel()
	hash, err := HashPassword("correct horse battery staple", testParams)
	assert.Must(t, err == nil, "HashPassword: %v", err)
	assert.Must(t, strings.HasPrefix(hash, "$argon2id$"), "hash = %q, want an argon2id encoded string", hash)

	ok, err := VerifyPassword("correct horse battery staple", hash)
	assert.Must(t, err == nil, "VerifyPassword: %v", err)
	assert.Must(t, ok, "the correct password did not verify")

	ok, err = VerifyPassword("wrong password", hash)
	assert.Must(t, err == nil, "VerifyPassword: %v", err)
	assert.Must(t, !ok, "a wrong password verified")
}

func TestHashIsSalted(t *testing.T) {
	t.Parallel()
	a, err := HashPassword("same", testParams)
	assert.Must(t, err == nil, "HashPassword: %v", err)
	b, err := HashPassword("same", testParams)
	assert.Must(t, err == nil, "HashPassword: %v", err)
	assert.Must(t, a != b, "two hashes of the same password are identical; the salt is not random")
}

// TestVerifyRejectsZeroCostParameters guards against a panic: argon2.IDKey
// panics if time or threads is less than 1, so a corrupted or maliciously
// crafted hash string must never reach it with either at zero.
func TestVerifyRejectsZeroCostParameters(t *testing.T) {
	t.Parallel()
	cases := []string{
		"$argon2id$v=19$m=8192,t=0,p=1$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		"$argon2id$v=19$m=8192,t=1,p=0$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
	}
	for _, encoded := range cases {
		if _, err := VerifyPassword("x", encoded); err == nil {
			t.Fatalf("VerifyPassword(%q): expected an error, got nil", encoded)
		}
	}
}

// TestVerifyRejectsMalformedParameterField guards the parser's totality:
// fmt.Sscanf, used previously, accepts trailing garbage after a matched
// conversion and skips leading whitespace before one, so it would silently
// accept inputs like these instead of rejecting them.
func TestVerifyRejectsMalformedParameterField(t *testing.T) {
	t.Parallel()
	salt := base64.RawStdEncoding.EncodeToString(make([]byte, 16))
	key := base64.RawStdEncoding.EncodeToString(make([]byte, 32))
	cases := []string{
		"$argon2id$v=19$m=8192,t=1,p=1XXXX$" + salt + "$" + key, // trailing garbage after p=
		"$argon2id$v=19junk$m=8192,t=1,p=1$" + salt + "$" + key, // trailing garbage after v=
		"$argon2id$v=19$m= 8192,t=1,p=1$" + salt + "$" + key,    // leading whitespace before m=
	}
	for _, encoded := range cases {
		if _, err := VerifyPassword("x", encoded); err == nil {
			t.Fatalf("VerifyPassword(%q): expected an error, got nil", encoded)
		}
	}
}

// TestVerifyRejectsFlippedSaltByte confirms that tampering with a stored
// hash by a single byte fails closed: no panic, no error, just ok=false.
func TestVerifyRejectsFlippedSaltByte(t *testing.T) {
	t.Parallel()
	hash, err := HashPassword("flip me", testParams)
	assert.Must(t, err == nil, "HashPassword: %v", err)

	parts := strings.Split(hash, "$")
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	assert.Must(t, err == nil, "decode salt: %v", err)
	salt[0] ^= 0xFF
	parts[4] = base64.RawStdEncoding.EncodeToString(salt)
	tampered := strings.Join(parts, "$")

	ok, err := VerifyPassword("flip me", tampered)
	assert.Must(t, err == nil, "VerifyPassword: %v", err)
	assert.Must(t, !ok, "a password verified against a hash with a flipped salt byte")
}

// TestHashAndVerifyAtProductionParameters exercises what the product
// actually ships: every other test in this file uses cheap parameters
// (t=1, m=8MiB) so tests run fast, but none of them exercise the real
// production cost (t=3, m=64MiB, p=2).
func TestHashAndVerifyAtProductionParameters(t *testing.T) {
	t.Parallel()
	prod := config.Argon2Params{Time: 3, Memory: 64 * 1024, Threads: 2, KeyLen: 32, SaltLen: 16}

	hash, err := HashPassword("correct horse battery staple", prod)
	assert.Must(t, err == nil, "HashPassword: %v", err)

	ok, err := VerifyPassword("correct horse battery staple", hash)
	assert.Must(t, err == nil, "VerifyPassword: %v", err)
	assert.Must(t, ok, "the correct password did not verify at production cost parameters")

	ok, err = VerifyPassword("wrong password", hash)
	assert.Must(t, err == nil, "VerifyPassword: %v", err)
	assert.Must(t, !ok, "a wrong password verified at production cost parameters")
}

func TestHashPasswordRejectsZeroCostParameters(t *testing.T) {
	t.Parallel()
	base := config.Argon2Params{Time: 1, Memory: 8 * 1024, Threads: 1, KeyLen: 32, SaltLen: 16}
	cases := []config.Argon2Params{
		{Time: 0, Memory: base.Memory, Threads: base.Threads, KeyLen: base.KeyLen, SaltLen: base.SaltLen},
		{Time: base.Time, Memory: base.Memory, Threads: 0, KeyLen: base.KeyLen, SaltLen: base.SaltLen},
		{Time: base.Time, Memory: base.Memory, Threads: base.Threads, KeyLen: 0, SaltLen: base.SaltLen},
		{Time: base.Time, Memory: base.Memory, Threads: base.Threads, KeyLen: base.KeyLen, SaltLen: 0},
	}
	for _, p := range cases {
		if _, err := HashPassword("x", p); err == nil {
			t.Fatalf("HashPassword(%+v): expected an error, got nil", p)
		}
	}
}

func TestNeedsRehash(t *testing.T) {
	t.Parallel()
	hash, err := HashPassword("x", testParams)
	assert.Must(t, err == nil, "HashPassword: %v", err)

	stale, err := NeedsRehash(hash, testParams)
	assert.Must(t, err == nil, "NeedsRehash: %v", err)
	assert.Must(t, !stale, "a hash produced with the current parameters reports needing a rehash")

	stronger := config.Argon2Params{Time: 3, Memory: 64 * 1024, Threads: 2, KeyLen: 32, SaltLen: 16}
	stale, err = NeedsRehash(hash, stronger)
	assert.Must(t, err == nil, "NeedsRehash: %v", err)
	assert.Must(t, stale, "a hash produced with weaker parameters does not report needing a rehash")

	if _, err := NeedsRehash("not-a-hash", testParams); err == nil {
		t.Fatal("expected an error for a malformed hash")
	}
}

// FuzzVerifyPassword fuzzes a hand-written parser over untrusted bytes, the
// textbook case for fuzzing. It enforces two invariants: VerifyPassword must
// never panic on any input (enforced implicitly — a panic fails the fuzz
// run), and it must never report a wrong password as verifying against a
// known-good hash.
func FuzzVerifyPassword(f *testing.F) {
	const correctPassword = "fuzzing correct horse battery staple"
	hash, err := HashPassword(correctPassword, testParams)
	if err != nil {
		f.Fatalf("HashPassword: %v", err)
	}

	seeds := []string{
		"not-a-hash",
		"",
		"$",
		"$argon2id$v=19$m=8192,t=1,p=1$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		"$argon2id$v=19$m=8192,t=0,p=1$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		"$argon2id$v=19$m=4294967295,t=1,p=1$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		"$argon2id$v=19$m=8192,t=1,p=1$$",
		hash,
	}
	for _, s := range seeds {
		f.Add(s, correctPassword)
		f.Add(s, "wrong password")
	}

	f.Fuzz(func(t *testing.T, encoded, password string) {
		ok, err := VerifyPassword(password, encoded)
		assert.Must(t, err == nil || !ok, "VerifyPassword(%q, %q) returned ok=true alongside a non-nil error", password, encoded)
		if encoded == hash {
			want := password == correctPassword
			assert.Must(t, ok == want, "VerifyPassword(%q, hash-of-%q) = %v, want %v", password, correctPassword, ok, want)
		}
	})
}

// Every encoded hash VerifyPassword refuses. None of them may verify a
// password: a hash this function cannot parse must never be an answer of
// "yes", whatever was supplied against it.
func TestVerifyRefusesAMalformedHash(t *testing.T) {
	t.Parallel()
	b64 := func(n int) string { return base64.RawStdEncoding.EncodeToString(make([]byte, n)) }
	hash := func(salt, key string) string {
		return "$argon2id$v=19$m=8192,t=1,p=1$" + salt + "$" + key
	}
	for _, tc := range []struct{ name, encoded string }{
		{"not a hash at all", "not-a-hash"},
		{"an excessive memory parameter",
			"$argon2id$v=19$m=4294967295,t=1,p=1$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"},
		{"an unsupported argon2 version",
			"$argon2id$v=16$m=8192,t=1,p=1$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"},
		{"an empty key", hash(b64(16), "")},
		{"an empty salt", hash("", b64(32))},
		{"an oversized salt", hash(b64(maxDecodedSaltLen+1), b64(32))},
		{"an oversized key", hash(b64(16), b64(maxDecodedKeyLen+1))},
		{"an undersized salt", hash(b64(minDecodedSaltLen-1), b64(32))},
		{"an undersized key", hash(b64(16), b64(minDecodedKeyLen-1))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ok, err := VerifyPassword("literally anything", tc.encoded)
			assert.Must(t, err != nil, "the hash was accepted")
			assert.Must(t, !ok, "a password verified against a hash this function cannot parse")
		})
	}
}
