package config

import (
	"os"
	"strings"
)

type Config struct {
	Port           string
	DatabaseURL    string
	RedisURL       string
	TMDBAPIKey     string
	TorrServerURL  string
	JackettURL     string
	JackettAPIKey  string
	AllowedOrigins []string
	// Extra Host names the server answers to (LAN names and IPs are always allowed)
	AllowedHosts []string
}

func Load() Config {
	return Config{
		Port:           getEnv("PORT", "8252"),
		DatabaseURL:    getEnv("DATABASE_URL", "./nextflix.db"),
		RedisURL:       getEnv("REDIS_URL", "redis://localhost:6379/0"),
		TMDBAPIKey:     os.Getenv("TMDB_API_KEY"),
		TorrServerURL:  getEnv("TORRSERVER_URL", "http://localhost:8090"),
		JackettURL:     getEnv("JACKETT_URL", "http://localhost:9117"),
		JackettAPIKey:  os.Getenv("JACKETT_API_KEY"),
		AllowedOrigins: splitList(os.Getenv("ALLOWED_ORIGINS")),
		AllowedHosts:   splitList(os.Getenv("ALLOWED_HOSTS")),
	}
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
