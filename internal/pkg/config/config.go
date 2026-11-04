package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/rs/zerolog"
)

type Config struct {
	LogFormat string
	LogLevel  zerolog.Level
}

var Cfg *Config

// Load reads process logging settings once before command execution. LOG_LEVEL controls verbosity independently of LOG_FORMAT.
func Load() error {
	level, err := zerolog.ParseLevel(strings.ToLower(getEnvOrDefault("LOG_LEVEL", "info")))
	if err != nil {
		return fmt.Errorf("parse LOG_LEVEL: %w", err)
	}
	format := getEnvOrDefault("LOG_FORMAT", "console")
	if format != "console" && format != "json" {
		return fmt.Errorf("invalid LOG_FORMAT %q: want console or json", format)
	}
	Cfg = &Config{LogFormat: format, LogLevel: level}
	return nil
}

func getEnvOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
