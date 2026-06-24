package handlers

import "net/http"

// CORS returns middleware that lets the given browser origin (the Next.js dev
// server, http://localhost:3000 by default) call the API from JavaScript. It sets
// the headers a browser requires and answers the preflight OPTIONS request
// itself. We allow a single configured origin rather than "*" so the policy is
// explicit and ready to point at a real dashboard origin in production.
func CORS(allowedOrigin string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Access-Control-Allow-Origin", allowedOrigin)
			w.Header().Add("Vary", "Origin") // response depends on Origin; keep caches honest
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

			// Preflight: the browser is only asking whether the real request is
			// allowed. Answer with the headers above and no body.
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// notFound and methodNotAllowed render chi's routing misses as our standard JSON
// error shape instead of plain text, so every API response has a consistent body.
func (h *Handler) notFound(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusNotFound, "resource not found")
}

func (h *Handler) methodNotAllowed(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusMethodNotAllowed, "method not allowed")
}
