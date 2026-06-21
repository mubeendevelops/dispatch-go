package handlers

import (
	"encoding/json"
	"log"
	"net/http"
)

// writeJSON encodes v as JSON with a consistent content type and status code.
// All API responses go through here so the shape stays uniform.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// Headers are already sent at this point; just log it.
		log.Printf("writeJSON: %v", err)
	}
}

// errorResponse is the single error shape for the whole API: {"error": "..."}.
type errorResponse struct {
	Error string `json:"error"`
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, errorResponse{Error: msg})
}
