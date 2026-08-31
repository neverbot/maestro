package identity

import (
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
