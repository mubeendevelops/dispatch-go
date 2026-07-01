package auth

import (
	"strings"
	"testing"
)

// TestPasswordHashRoundTrip pins the core password contract: a hash is not the
// plaintext, the right password verifies, and a wrong one does not.
func TestPasswordHashRoundTrip(t *testing.T) {
	const pw = "correct horse battery staple"
	hash, err := HashPassword(pw)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if hash == pw {
		t.Fatal("hash equals plaintext")
	}
	if err := CheckPassword(hash, pw); err != nil {
		t.Errorf("CheckPassword on correct password: %v", err)
	}
	if err := CheckPassword(hash, "wrong"); err == nil {
		t.Error("CheckPassword accepted a wrong password")
	}
}

// TestPasswordHashSalted proves bcrypt salts internally: the same password hashes
// to different strings, so a stolen hash reveals nothing about reuse.
func TestPasswordHashSalted(t *testing.T) {
	h1, _ := HashPassword("same")
	h2, _ := HashPassword("same")
	if h1 == h2 {
		t.Error("two hashes of the same password are identical (missing salt?)")
	}
}

// TestGenerateSessionTokenUnique checks tokens are non-empty and don't repeat --
// a collision would mean two users could share a session.
func TestGenerateSessionTokenUnique(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		tok, err := GenerateSessionToken()
		if err != nil {
			t.Fatalf("GenerateSessionToken: %v", err)
		}
		if tok == "" {
			t.Fatal("empty token")
		}
		if seen[tok] {
			t.Fatal("duplicate session token")
		}
		seen[tok] = true
	}
}

// TestGenerateAPIKeyShape checks the key carries the scheme, the display prefix is
// a true prefix of the full key, and the prefix is not the whole secret.
func TestGenerateAPIKeyShape(t *testing.T) {
	full, prefix, err := GenerateAPIKey()
	if err != nil {
		t.Fatalf("GenerateAPIKey: %v", err)
	}
	if !strings.HasPrefix(full, APIKeyPrefix) {
		t.Errorf("full key %q missing scheme %q", full, APIKeyPrefix)
	}
	if !strings.HasPrefix(full, prefix) {
		t.Errorf("full key %q does not start with its display prefix %q", full, prefix)
	}
	if prefix == full {
		t.Error("display prefix must not equal the full key (would leak the secret)")
	}
}

// TestHashAPIKeyDeterministic checks the API-key hash is stable (so a presented
// key can be looked up by hash), correctly sized, and collision-distinct.
func TestHashAPIKeyDeterministic(t *testing.T) {
	full, _, _ := GenerateAPIKey()
	if HashAPIKey(full) != HashAPIKey(full) {
		t.Error("HashAPIKey not deterministic")
	}
	if got := len(HashAPIKey(full)); got != 64 {
		t.Errorf("HashAPIKey length = %d, want 64 hex chars", got)
	}
	other, _, _ := GenerateAPIKey()
	if HashAPIKey(full) == HashAPIKey(other) {
		t.Error("distinct keys hashed to the same value")
	}
}

// TestHashSessionTokenKeyed proves the pepper actually keys the hash: the same
// token under different secrets yields different hashes, and it's deterministic
// for a fixed secret (so a cookie token can be looked up by hash).
func TestHashSessionTokenKeyed(t *testing.T) {
	tok, _ := GenerateSessionToken()
	a := HashSessionToken([]byte("secret-A"), tok)
	b := HashSessionToken([]byte("secret-B"), tok)
	if a == b {
		t.Error("HMAC pepper has no effect: different secrets produced the same hash")
	}
	if a != HashSessionToken([]byte("secret-A"), tok) {
		t.Error("HashSessionToken not deterministic for a fixed secret")
	}
	if got := len(a); got != 64 {
		t.Errorf("HashSessionToken length = %d, want 64 hex chars", got)
	}
}
