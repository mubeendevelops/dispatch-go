package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/mubeendevelops/dispatch-go/internal/auth"
	"github.com/mubeendevelops/dispatch-go/internal/models"
	"github.com/mubeendevelops/dispatch-go/internal/store"
)

const maxKeyNameLen = 100

// createKeyRequest is the POST /api/v1/keys body.
type createKeyRequest struct {
	Name string `json:"name"`
}

// createKeyResponse is returned ONCE, at creation: it is the only time the full
// plaintext key is ever revealed. Thereafter only the prefix + metadata are
// listable, and the full key is unrecoverable (we stored just its hash).
type createKeyResponse struct {
	ID        uuid.UUID `json:"id"`
	Name      string    `json:"name"`
	Key       string    `json:"key"` // full plaintext -- shown once, never recoverable
	KeyPrefix string    `json:"key_prefix"`
	CreatedAt time.Time `json:"created_at"`
}

// keyListResponse is GET /api/v1/keys.
type keyListResponse struct {
	Keys []models.APIKey `json:"keys"`
}

// listKeys returns the caller tenant's API keys (metadata only -- never a hash or
// plaintext).
func (h *Handler) listKeys(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := h.tenantID(w, r)
	if !ok {
		return
	}
	keys, err := h.store.ListAPIKeys(r.Context(), tenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list API keys")
		return
	}
	writeJSON(w, http.StatusOK, keyListResponse{Keys: keys})
}

// createKey mints a new API key for the caller's tenant and returns the full key
// exactly once. Only its SHA-256 hash + display prefix are stored.
func (h *Handler) createKey(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := h.tenantID(w, r)
	if !ok {
		return
	}
	var req createKeyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if len(req.Name) > maxKeyNameLen {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("name must be at most %d characters", maxKeyNameLen))
		return
	}

	full, prefix, err := auth.GenerateAPIKey()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to generate key")
		return
	}
	key, err := h.store.CreateAPIKey(r.Context(), tenantID, req.Name, auth.HashAPIKey(full), prefix)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create key")
		return
	}
	// The full plaintext key is returned here and NOWHERE else -- we persisted only
	// its hash, so this response is the user's one chance to copy it.
	writeJSON(w, http.StatusCreated, createKeyResponse{
		ID:        key.ID,
		Name:      key.Name,
		Key:       full,
		KeyPrefix: key.KeyPrefix,
		CreatedAt: key.CreatedAt,
	})
}

// deleteKey revokes one of the caller tenant's keys. The store scopes the delete by
// tenant, so a key owned by another tenant reads as "not found" -- one tenant can
// neither delete nor probe for another's keys.
func (h *Handler) deleteKey(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := h.tenantID(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid key id")
		return
	}
	if err := h.store.DeleteAPIKey(r.Context(), tenantID, id); err != nil {
		if errors.Is(err, store.ErrAPIKeyNotFound) {
			writeError(w, http.StatusNotFound, "key not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to delete key")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
