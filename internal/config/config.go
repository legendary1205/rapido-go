// Package config loads panel configuration from the process environment.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Role string

const (
	RoleAPI     Role = "api"
	RoleBackend Role = "backend"
)

type Config struct {
	Role Role

	HTTPHost string
	HTTPPort int

	DatabaseURL string
	RedisAddr   string
	RedisPass   string
	RedisDB     int

	JWTAccessTTL time.Duration

	SudoUsername string
	SudoPassword string

	CertsDir string

	// AllowedOrigins mirrors ALLOWED_ORIGINS in the current config.py
	// (comma-separated, defaulting to "*").
	AllowedOrigins []string
}

func Load() (*Config, error) {
	cfg := &Config{
		Role:           Role(getEnv("ROLE", string(RoleAPI))),
		HTTPHost:       getEnv("UVICORN_HOST", "0.0.0.0"),
		DatabaseURL:    getEnv("DATABASE_URL", ""),
		RedisAddr:      getEnv("REDIS_ADDR", "127.0.0.1:6379"),
		RedisPass:      getEnv("REDIS_PASSWORD", ""),
		SudoUsername:   getEnv("SUDO_USERNAME", ""),
		SudoPassword:   getEnv("SUDO_PASSWORD", ""),
		CertsDir:       getEnv("CERTS_DIR", "./certs"),
		JWTAccessTTL:   24 * time.Hour,
		AllowedOrigins: strings.Split(getEnv("ALLOWED_ORIGINS", "*"), ","),
	}

	if cfg.Role != RoleAPI && cfg.Role != RoleBackend {
		return nil, fmt.Errorf("config: invalid ROLE %q, expected %q or %q", cfg.Role, RoleAPI, RoleBackend)
	}

	port, err := strconv.Atoi(getEnv("UVICORN_PORT", "8000"))
	if err != nil {
		return nil, fmt.Errorf("config: invalid UVICORN_PORT: %w", err)
	}
	cfg.HTTPPort = port

	redisDB, err := strconv.Atoi(getEnv("REDIS_DB", "0"))
	if err != nil {
		return nil, fmt.Errorf("config: invalid REDIS_DB: %w", err)
	}
	cfg.RedisDB = redisDB

	if cfg.DatabaseURL == "" {
		return nil, fmt.Errorf("config: DATABASE_URL is required")
	}

	return cfg, nil
}

func getEnv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}
