package identity

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/neverbot/maestro/internal/config"
)

var testParams = config.Argon2Params{Time: 1, Memory: 8 * 1024, Threads: 1, KeyLen: 32, SaltLen: 16}

func TestHashAndVerify(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple", testParams)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if !strings.HasPrefix(hash, "$argon2id$") {
		t.Fatalf("hash = %q, want an argon2id encoded string", hash)
	}

	ok, err := VerifyPassword("correct horse battery staple", hash)
	if err != nil {
		t.Fatalf("VerifyPassword: %v", err)
	}
	if !ok {
		t.Fatal("the correct password did not verify")
	}

	ok, err = VerifyPassword("wrong password", hash)
	if err != nil {
		t.Fatalf("VerifyPassword: %v", err)
	}
	if ok {
		t.Fatal("a wrong password verified")
	}
}

func TestHashIsSalted(t *testing.T) {
	a, err := HashPassword("same", testParams)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	b, err := HashPassword("same", testParams)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if a == b {
		t.Fatal("two hashes of the same password are identical; the salt is not random")
	}
}

func TestVerifyRejectsMalformedHash(t *testing.T) {
	if _, err := VerifyPassword("x", "not-a-hash"); err == nil {
		t.Fatal("expected an error for a malformed hash")
	}
}

// TestVerifyRejectsZeroCostParameters guards against a panic: argon2.IDKey
// panics if time or threads is less than 1, so a corrupted or maliciously
// crafted hash string must never reach it with either at zero.
func TestVerifyRejectsZeroCostParameters(t *testing.T) {
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

// TestVerifyRejectsExcessiveMemory guards against a resource-exhaustion
// vector: without a ceiling, a crafted "m=" value would make VerifyPassword
// allocate an unbounded amount of memory before any comparison happens.
func TestVerifyRejectsExcessiveMemory(t *testing.T) {
	encoded := "$argon2id$v=19$m=4294967295,t=1,p=1$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	if _, err := VerifyPassword("x", encoded); err == nil {
		t.Fatal("expected an error for an excessive memory parameter")
	}
}

// TestVerifyRejectsUnsupportedVersion guards against silently comparing
// against a different argon2 version than this package produces.
func TestVerifyRejectsUnsupportedVersion(t *testing.T) {
	encoded := "$argon2id$v=16$m=8192,t=1,p=1$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	if _, err := VerifyPassword("x", encoded); err == nil {
		t.Fatal("expected an error for an unsupported argon2 version")
	}
}

// TestVerifyRejectsEmptyKey guards against an authentication bypass: without
// this check, a hash string ending in "$<salt>$" decodes to a zero-length
// key, argon2.IDKey with keyLen=0 returns a zero-length slice, and
// subtle.ConstantTimeCompare reports two zero-length slices as equal, so
// every password would verify against such a row.
func TestVerifyRejectsEmptyKey(t *testing.T) {
	salt := base64.RawStdEncoding.EncodeToString(make([]byte, 16))
	encoded := "$argon2id$v=19$m=8192,t=1,p=1$" + salt + "$"

	ok, err := VerifyPassword("literally anything", encoded)
	if err == nil {
		t.Fatal("expected an error for an empty key")
	}
	if ok {
		t.Fatal("a password verified against a hash with an empty key")
	}
}

// TestVerifyRejectsEmptySalt guards against the same class of malformed
// input on the salt field, for symmetry with the empty-key check.
func TestVerifyRejectsEmptySalt(t *testing.T) {
	key := base64.RawStdEncoding.EncodeToString(make([]byte, 32))
	encoded := "$argon2id$v=19$m=8192,t=1,p=1$$" + key

	if _, err := VerifyPassword("x", encoded); err == nil {
		t.Fatal("expected an error for an empty salt")
	}
}

// TestVerifyRejectsOversizedSalt and TestVerifyRejectsOversizedKey guard
// against a resource-exhaustion vector on the two axes adjacent to the "m="
// check: without a ceiling, a crafted hash with an oversized base64 salt or
// key field forces a correspondingly large allocation before any comparison
// happens (the key length in particular is passed straight through as
// argon2.IDKey's keyLen).
func TestVerifyRejectsOversizedSalt(t *testing.T) {
	salt := base64.RawStdEncoding.EncodeToString(make([]byte, maxDecodedSaltLen+1))
	key := base64.RawStdEncoding.EncodeToString(make([]byte, 32))
	encoded := "$argon2id$v=19$m=8192,t=1,p=1$" + salt + "$" + key

	if _, err := VerifyPassword("x", encoded); err == nil {
		t.Fatal("expected an error for an oversized salt")
	}
}

func TestVerifyRejectsOversizedKey(t *testing.T) {
	salt := base64.RawStdEncoding.EncodeToString(make([]byte, 16))
	key := base64.RawStdEncoding.EncodeToString(make([]byte, maxDecodedKeyLen+1))
	encoded := "$argon2id$v=19$m=8192,t=1,p=1$" + salt + "$" + key

	if _, err := VerifyPassword("x", encoded); err == nil {
		t.Fatal("expected an error for an oversized key")
	}
}

// TestVerifyRejectsUndersizedSalt and TestVerifyRejectsUndersizedKey guard
// against a partial authentication bypass, the same class as the empty-key
// bypass above but non-total: a key or salt shorter than the minimum still
// narrows the comparison enough for a wrong password to match by chance
// (a 1-byte key matches roughly 1 wrong password in 256), which is an
// authentication weakness rather than a mere robustness gap.
func TestVerifyRejectsUndersizedSalt(t *testing.T) {
	salt := base64.RawStdEncoding.EncodeToString(make([]byte, minDecodedSaltLen-1))
	key := base64.RawStdEncoding.EncodeToString(make([]byte, 32))
	encoded := "$argon2id$v=19$m=8192,t=1,p=1$" + salt + "$" + key

	if _, err := VerifyPassword("x", encoded); err == nil {
		t.Fatal("expected an error for an undersized salt")
	}
}

func TestVerifyRejectsUndersizedKey(t *testing.T) {
	salt := base64.RawStdEncoding.EncodeToString(make([]byte, 16))
	key := base64.RawStdEncoding.EncodeToString(make([]byte, minDecodedKeyLen-1))
	encoded := "$argon2id$v=19$m=8192,t=1,p=1$" + salt + "$" + key

	ok, err := VerifyPassword("x", encoded)
	if err == nil {
		t.Fatal("expected an error for an undersized key")
	}
	if ok {
		t.Fatal("a password verified against a hash with an undersized key")
	}
}

// TestVerifyRejectsMalformedParameterField guards the parser's totality:
// fmt.Sscanf, used previously, accepts trailing garbage after a matched
// conversion and skips leading whitespace before one, so it would silently
// accept inputs like these instead of rejecting them.
func TestVerifyRejectsMalformedParameterField(t *testing.T) {
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
	hash, err := HashPassword("flip me", testParams)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}

	parts := strings.Split(hash, "$")
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		t.Fatalf("decode salt: %v", err)
	}
	salt[0] ^= 0xFF
	parts[4] = base64.RawStdEncoding.EncodeToString(salt)
	tampered := strings.Join(parts, "$")

	ok, err := VerifyPassword("flip me", tampered)
	if err != nil {
		t.Fatalf("VerifyPassword: %v", err)
	}
	if ok {
		t.Fatal("a password verified against a hash with a flipped salt byte")
	}
}

// TestHashAndVerifyAtProductionParameters exercises what the product
// actually ships: every other test in this file uses cheap parameters
// (t=1, m=8MiB) so tests run fast, but none of them exercise the real
// production cost (t=3, m=64MiB, p=2).
func TestHashAndVerifyAtProductionParameters(t *testing.T) {
	prod := config.Argon2Params{Time: 3, Memory: 64 * 1024, Threads: 2, KeyLen: 32, SaltLen: 16}

	hash, err := HashPassword("correct horse battery staple", prod)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}

	ok, err := VerifyPassword("correct horse battery staple", hash)
	if err != nil {
		t.Fatalf("VerifyPassword: %v", err)
	}
	if !ok {
		t.Fatal("the correct password did not verify at production cost parameters")
	}

	ok, err = VerifyPassword("wrong password", hash)
	if err != nil {
		t.Fatalf("VerifyPassword: %v", err)
	}
	if ok {
		t.Fatal("a wrong password verified at production cost parameters")
	}
}

func TestHashPasswordRejectsZeroCostParameters(t *testing.T) {
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
	hash, err := HashPassword("x", testParams)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}

	stale, err := NeedsRehash(hash, testParams)
	if err != nil {
		t.Fatalf("NeedsRehash: %v", err)
	}
	if stale {
		t.Fatal("a hash produced with the current parameters reports needing a rehash")
	}

	stronger := config.Argon2Params{Time: 3, Memory: 64 * 1024, Threads: 2, KeyLen: 32, SaltLen: 16}
	stale, err = NeedsRehash(hash, stronger)
	if err != nil {
		t.Fatalf("NeedsRehash: %v", err)
	}
	if !stale {
		t.Fatal("a hash produced with weaker parameters does not report needing a rehash")
	}

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
		if err != nil && ok {
			t.Fatalf("VerifyPassword(%q, %q) returned ok=true alongside a non-nil error", password, encoded)
		}
		if encoded == hash {
			want := password == correctPassword
			if ok != want {
				t.Fatalf("VerifyPassword(%q, hash-of-%q) = %v, want %v", password, correctPassword, ok, want)
			}
		}
	})
}
