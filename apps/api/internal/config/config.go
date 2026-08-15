package config

import (
	"fmt"
	"os"
	"strconv"
)

type Config struct {
	Environment    string
	PublicAddress  string
	PrivateAddress string
	AssetDirectory string
	FirestoreHost  string
	LocalProxyAuth bool
}

func Load() (Config, error) {
	localProxyAuth, err := boolEnv("CONTENTFLOW_LOCAL_PROXY_AUTH", false)
	if err != nil {
		return Config{}, err
	}

	cfg := Config{
		Environment:    stringEnv("CONTENTFLOW_ENV", "development"),
		PublicAddress:  stringEnv("CONTENTFLOW_ADDR", ":8080"),
		PrivateAddress: stringEnv("CONTENTFLOW_PRIVATE_ADDR", ":8081"),
		AssetDirectory: stringEnv("CONTENTFLOW_ASSET_DIR", "var/assets"),
		FirestoreHost:  os.Getenv("FIRESTORE_EMULATOR_HOST"),
		LocalProxyAuth: localProxyAuth,
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) Validate() error {
	if c.Environment == "production" && c.LocalProxyAuth {
		return fmt.Errorf("CONTENTFLOW_LOCAL_PROXY_AUTH cannot be enabled in production")
	}
	if c.PublicAddress == "" {
		return fmt.Errorf("CONTENTFLOW_ADDR is required")
	}
	if c.AssetDirectory == "" {
		return fmt.Errorf("CONTENTFLOW_ASSET_DIR is required")
	}
	if c.LocalProxyAuth && c.PrivateAddress == "" {
		return fmt.Errorf("CONTENTFLOW_PRIVATE_ADDR is required when local proxy authentication is enabled")
	}
	return nil
}

func stringEnv(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func boolEnv(name string, fallback bool) (bool, error) {
	value := os.Getenv(name)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("parse %s: %w", name, err)
	}
	return parsed, nil
}
