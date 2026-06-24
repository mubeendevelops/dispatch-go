// Package config loads runtime configuration from environment variables only
// (per CLAUDE.md). Defaults mirror docker-compose.yml so a fresh checkout runs
// without a .env file. The api, worker, and scheduler all share this loader.
package config

import (
	"os"
	"strconv"
	"strings"
)

// Config holds everything the services need to start.
type Config struct {
	DatabaseURL   string   // pgx connection string
	RedisAddr     string   // host:port
	RedisPassword string   // empty for local dev
	RedisDB       int      // Redis logical database number
	APIPort       string   // port the HTTP API listens on
	Queues        []string // queues the worker consumes from

	CORSAllowedOrigin string // browser origin allowed to call the API (the dashboard)
}

// Load reads configuration from the environment, applying sensible defaults.
func Load() Config {
	return Config{
		DatabaseURL:   getenv("DATABASE_URL", "postgres://dispatch:dispatch@localhost:5433/dispatch?sslmode=disable"),
		RedisAddr:     getenv("REDIS_ADDR", "localhost:6379"),
		RedisPassword: getenv("REDIS_PASSWORD", ""),
		RedisDB:       getenvInt("REDIS_DB", 0),
		APIPort:       getenv("API_PORT", "8080"),
		Queues:        getenvList("QUEUES", []string{"default"}),

		CORSAllowedOrigin: getenv("CORS_ALLOWED_ORIGIN", "http://localhost:3000"),
	}
}

func getenv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

func getenvInt(key string, fallback int) int {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

// getenvList parses a comma-separated value (e.g. QUEUES="default,emails").
func getenvList(key string, fallback []string) []string {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return fallback
	}
	out := make([]string, 0)
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return fallback
	}
	return out
}
