package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/mubeendevelops/dispatch-go/internal/auth"
	"github.com/mubeendevelops/dispatch-go/internal/models"
	"github.com/mubeendevelops/dispatch-go/internal/store"
)

// Input limits for the auth endpoints. maxPasswordLen caps at bcrypt's 72-byte
// input limit (beyond which bcrypt silently ignores the tail, so two different
// long passwords could collide) -- we reject rather than truncate. minPasswordLen
// is a modest usability floor; maxEmailLen is the RFC 5321 practical maximum.
const (
	minPasswordLen = 8
	maxPasswordLen = 72
	maxEmailLen    = 254
)

// credentialsRequest is the shared body of signup and login.
type credentialsRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// meResponse is GET /auth/me: the authenticated user plus their tenant.
type meResponse struct {
	User   *models.User   `json:"user"`
	Tenant *models.Tenant `json:"tenant"`
}

// signup provisions a new tenant + first user, logs them in (sets the session
// cookie), and returns the user. One user per tenant for MVP.
func (h *Handler) signup(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeCredentials(w, r)
	if !ok {
		return
	}

	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to process password")
		return
	}

	_, user, err := h.store.CreateAccount(r.Context(), req.Email, hash)
	if err != nil {
		if errors.Is(err, store.ErrEmailTaken) {
			writeError(w, http.StatusConflict, "email already registered")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to create account")
		return
	}

	if err := h.startSession(w, r, user.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "account created but failed to start session")
		return
	}
	writeJSON(w, http.StatusCreated, user)
}

// login verifies email + password and, on success, starts a session. It returns a
// single generic 401 for both an unknown email and a wrong password, and burns a
// dummy bcrypt compare on the unknown-email path so response timing doesn't reveal
// which emails are registered.
func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeCredentials(w, r)
	if !ok {
		return
	}

	user, err := h.store.GetUserByEmail(r.Context(), req.Email)
	if err != nil {
		if errors.Is(err, store.ErrUserNotFound) {
			auth.CheckDummyPassword(req.Password) // equalize timing vs. the found-user path
			writeError(w, http.StatusUnauthorized, "invalid email or password")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load user")
		return
	}
	if err := auth.CheckPassword(user.PasswordHash, req.Password); err != nil {
		writeError(w, http.StatusUnauthorized, "invalid email or password")
		return
	}

	if err := h.startSession(w, r, user.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start session")
		return
	}
	writeJSON(w, http.StatusOK, user)
}

// logout deletes the current session server-side (immediate revocation -- the
// whole point of DB-backed sessions) and clears the cookie. Idempotent.
func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(h.sessions.CookieName); err == nil {
		tokenHash := auth.HashSessionToken(h.sessions.Secret, c.Value)
		if err := h.store.DeleteSession(r.Context(), tokenHash); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to end session")
			return
		}
	}
	h.clearSessionCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

// me returns the authenticated user and their tenant.
func (h *Handler) me(w http.ResponseWriter, r *http.Request) {
	user, ok := h.user(w, r)
	if !ok {
		return
	}
	tenant, err := h.store.GetTenant(r.Context(), user.TenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load tenant")
		return
	}
	writeJSON(w, http.StatusOK, meResponse{User: user, Tenant: tenant})
}

// RequireSession authenticates a dashboard request by its session cookie: it hashes
// the cookie token, looks up the live session, and injects the resolved tenant AND
// user into the request context. A missing cookie or unknown/expired session is a
// generic 401 (we never say which, so a stale and a forged cookie are
// indistinguishable); a genuine store error is a 500.
func (h *Handler) RequireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(h.sessions.CookieName)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		tokenHash := auth.HashSessionToken(h.sessions.Secret, c.Value)
		user, err := h.store.SessionUser(r.Context(), tokenHash)
		if errors.Is(err, store.ErrSessionInvalid) {
			writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "authentication error")
			return
		}
		// Inject both: the tenant scopes the store, the user identifies the human.
		ctx := WithUser(WithTenant(r.Context(), user.TenantID), user)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequireAPIKey authenticates a programmatic request by its Bearer API key: it
// parses the Authorization header, requires the dk_ scheme (so obviously-wrong
// tokens are rejected before any DB work), hashes the key, resolves it to a tenant,
// and injects the tenant into the request context. A best-effort last_used_at touch
// follows. Every auth failure is a single generic 401 that never reveals whether a
// key existed.
func (h *Handler) RequireAPIKey(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key, ok := bearerToken(r.Header.Get("Authorization"))
		if !ok || !strings.HasPrefix(key, auth.APIKeyPrefix) {
			writeError(w, http.StatusUnauthorized, "invalid or missing API key")
			return
		}
		tenantID, keyID, err := h.store.AuthenticateAPIKey(r.Context(), auth.HashAPIKey(key))
		if errors.Is(err, store.ErrAPIKeyNotFound) {
			writeError(w, http.StatusUnauthorized, "invalid or missing API key")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "authentication error")
			return
		}
		// Best-effort usage stamp: never fail or block the request on it.
		if err := h.store.TouchAPIKey(r.Context(), keyID); err != nil {
			log.Printf("touch api key %s: %v", keyID, err)
		}
		next.ServeHTTP(w, r.WithContext(WithTenant(r.Context(), tenantID)))
	})
}

// startSession generates a fresh opaque token, stores its keyed hash with a TTL,
// and sets the session cookie to the plaintext token. Called by signup and login.
func (h *Handler) startSession(w http.ResponseWriter, r *http.Request, userID uuid.UUID) error {
	token, err := auth.GenerateSessionToken()
	if err != nil {
		return err
	}
	expiresAt := time.Now().Add(h.sessions.TTL)
	if err := h.store.CreateSession(r.Context(), userID, auth.HashSessionToken(h.sessions.Secret, token), expiresAt); err != nil {
		return err
	}
	h.setSessionCookie(w, token, expiresAt)
	return nil
}

// setSessionCookie writes the session cookie carrying the opaque token. The flags
// are the security-relevant part:
//   - HttpOnly: JavaScript can't read it, so an XSS can't exfiltrate the session.
//   - SameSite=Lax: the browser won't send it on cross-site subrequests -- a
//     baseline CSRF defense (all state-changing endpoints are POST/DELETE) -- while
//     still allowing top-level navigation.
//   - Secure (config-gated): only sent over HTTPS. Off for the local http demo, on
//     in production.
func (h *Handler) setSessionCookie(w http.ResponseWriter, token string, expiresAt time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     h.sessions.CookieName,
		Value:    token,
		Path:     "/",
		Expires:  expiresAt,
		MaxAge:   int(time.Until(expiresAt).Seconds()),
		HttpOnly: true,
		Secure:   h.sessions.Secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// clearSessionCookie overwrites the cookie with an immediately-expiring one so the
// browser drops it on logout.
func (h *Handler) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     h.sessions.CookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   h.sessions.Secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// decodeCredentials parses and validates a signup/login body, normalizing the
// email (trim + lowercase) so storage and lookup always agree. On any problem it
// writes a 400 and returns ok=false.
func decodeCredentials(w http.ResponseWriter, r *http.Request) (credentialsRequest, bool) {
	var req credentialsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return req, false
	}
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))
	if !validEmail(req.Email) {
		writeError(w, http.StatusBadRequest, "a valid email is required")
		return req, false
	}
	if len(req.Password) < minPasswordLen {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("password must be at least %d characters", minPasswordLen))
		return req, false
	}
	if len(req.Password) > maxPasswordLen {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("password must be at most %d characters", maxPasswordLen))
		return req, false
	}
	return req, true
}

// validEmail is a deliberately minimal sanity check (non-empty, a local part, an
// @, a dotted space-free domain, within length). Full RFC 5322 validation is
// famously not worth it; the real proof an address works is sending mail, which is
// out of MVP scope.
func validEmail(email string) bool {
	if email == "" || len(email) > maxEmailLen {
		return false
	}
	at := strings.IndexByte(email, '@')
	if at <= 0 || at == len(email)-1 {
		return false
	}
	domain := email[at+1:]
	return strings.Contains(domain, ".") && !strings.Contains(domain, " ")
}

// bearerToken extracts the token from an "Authorization: Bearer <token>" header.
// The scheme is matched case-insensitively (per RFC 7235); ok is false if the
// header is missing, uses another scheme, or carries an empty token.
func bearerToken(header string) (string, bool) {
	const prefix = "bearer "
	if len(header) < len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return "", false
	}
	token := strings.TrimSpace(header[len(prefix):])
	if token == "" {
		return "", false
	}
	return token, true
}
