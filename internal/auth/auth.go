// Package auth is the single home for the project's credential cryptography, so
// every hashing and token-generation decision lives in one reviewable place
// (plan.md §2.7). It deliberately holds THREE schemes for three kinds of secret,
// because they have different threat models:
//
//   - Passwords (low-entropy, human-chosen) -> bcrypt: a slow, salted, adaptive
//     KDF, so an offline crack of a stolen hash is expensive.
//   - API keys (256-bit random) -> SHA-256: a fast one-way hash is safe because the
//     input is already high-entropy, and being deterministic it allows an O(1)
//     indexed lookup by hash. bcrypt would be unusable here -- per-hash salts mean
//     you could not look a key up by its hash.
//   - Session tokens (256-bit random) -> HMAC-SHA256 keyed by a server-side pepper:
//     like API keys but keyed, so a read-only DB leak of the hash column is useless
//     without the separately-held secret. Sessions are ephemeral, so coupling them
//     to a rotatable secret is fine (rotating = global logout).
//
// The store never sees a plaintext credential: callers hash here and pass the
// opaque hash string down, so package store stays pure SQL.
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"

	"golang.org/x/crypto/bcrypt"
)

// APIKeyPrefix is the scheme every generated API key starts with, so a key is
// recognizable at a glance and the middleware can reject an obviously-wrong token
// before touching the database. "dk" = dispatch key.
const APIKeyPrefix = "dk_"

// tokenBytes is the entropy, in bytes, of a generated session token or API key.
// 32 bytes = 256 bits: far beyond guessable, which is exactly why a fast hash
// (not a slow KDF) is the right way to store the derived value.
const tokenBytes = 32

// prefixDisplayLen is how many characters of a generated key (after the scheme) we
// keep as the non-secret display prefix, e.g. the "ab12cd34" in dk_ab12cd34.
const prefixDisplayLen = 8

// dummyPasswordHash is a valid bcrypt hash computed once at startup. The login
// path compares against it when no user matched the submitted email, so an
// unknown-email attempt still pays the same bcrypt cost as a real one -- closing
// the timing side-channel that would otherwise reveal which emails are registered.
var dummyPasswordHash []byte

func init() {
	// Cost matches HashPassword so the dummy compare takes the same time as a real
	// one. bcrypt of a constant cannot fail in practice, so a failure here is a
	// programming error worth surfacing loudly at startup.
	h, err := bcrypt.GenerateFromPassword([]byte("dispatch-timing-equalizer"), bcrypt.DefaultCost)
	if err != nil {
		panic(fmt.Sprintf("auth: init dummy hash: %v", err))
	}
	dummyPasswordHash = h
}

// HashPassword returns a bcrypt hash of a plaintext password, suitable for
// storage. bcrypt salts internally, so two calls on the same password yield
// different hashes. NOTE: bcrypt ignores input past 72 bytes; acceptable for this
// project (documented rather than pre-hashed -- see the login length cap).
func HashPassword(plaintext string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(plaintext), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}
	return string(h), nil
}

// CheckPassword reports whether plaintext matches a stored bcrypt hash: nil on a
// match, a non-nil error otherwise. The comparison is constant-time
// (bcrypt.CompareHashAndPassword), so it leaks no timing information.
func CheckPassword(hash, plaintext string) error {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plaintext))
}

// CheckDummyPassword burns a bcrypt comparison against a throwaway hash, for the
// login path to call when the email matched no user. It equalizes response time
// between "no such user" and "wrong password" so the endpoint doesn't reveal which
// emails exist. The result is intentionally discarded.
func CheckDummyPassword(plaintext string) {
	_ = bcrypt.CompareHashAndPassword(dummyPasswordHash, []byte(plaintext))
}

// GenerateSessionToken returns a new opaque session token: 256 bits of CSPRNG
// output, base64url-encoded for use as a cookie value. This is the PLAINTEXT that
// goes in the cookie; only its keyed hash (HashSessionToken) is stored.
func GenerateSessionToken() (string, error) {
	return randomToken()
}

// GenerateAPIKey returns a new API key. full is the plaintext shown to the caller
// exactly once (scheme + 256 bits of base64url CSPRNG); prefix is a short,
// non-secret slice kept for dashboard display. Only HashAPIKey(full) is stored.
func GenerateAPIKey() (full, prefix string, err error) {
	tok, err := randomToken()
	if err != nil {
		return "", "", err
	}
	full = APIKeyPrefix + tok
	// Display prefix: the scheme plus the first few token chars -- non-secret,
	// enough to recognize a key, far too little to guess it.
	prefix = APIKeyPrefix + tok[:prefixDisplayLen]
	return full, prefix, nil
}

// randomToken reads tokenBytes of cryptographic randomness and base64url-encodes
// it (no padding). A read error is fatal to the caller -- we NEVER fall back to a
// weaker source, because a predictable token is a full auth bypass.
func randomToken() (string, error) {
	b := make([]byte, tokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// HashAPIKey returns the hex SHA-256 of a full API key, for storage and lookup.
// Deterministic (so the middleware can look a presented key up by hash) and fast
// (safe because the key is 256-bit random). Not peppered, on purpose -- see the
// package doc and migration 0008.
func HashAPIKey(full string) string {
	sum := sha256.Sum256([]byte(full))
	return hex.EncodeToString(sum[:])
}

// HashSessionToken returns the hex HMAC-SHA256 of a session token under secret --
// the keyed "pepper" that makes a leaked token_hash column useless without the
// separately-held SESSION_SECRET. Deterministic, so the middleware can look a
// presented cookie token up by hash.
func HashSessionToken(secret []byte, token string) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(token))
	return hex.EncodeToString(mac.Sum(nil))
}
