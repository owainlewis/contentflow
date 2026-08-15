package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	Environment    string
	PublicAddress  string
	PrivateAddress string
	AssetDirectory string
	FirestoreHost  string
	LocalProxyAuth bool
	GoogleProject  string
	PublicOrigin   string
	OAuthIssuer    string
	OAuthClientID  string
	OAuthSecret    string
	OwnerSubject   string
	WorkspaceID    string
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
		GoogleProject:  os.Getenv("CONTENTFLOW_GOOGLE_PROJECT_ID"),
		PublicOrigin:   os.Getenv("CONTENTFLOW_PUBLIC_ORIGIN"),
		OAuthIssuer:    os.Getenv("CONTENTFLOW_OAUTH_ISSUER"),
		OAuthClientID:  os.Getenv("CONTENTFLOW_OAUTH_CLIENT_ID"),
		OAuthSecret:    os.Getenv("CONTENTFLOW_OAUTH_CLIENT_SECRET"),
		OwnerSubject:   os.Getenv("CONTENTFLOW_OWNER_SUBJECT"),
		WorkspaceID:    os.Getenv("CONTENTFLOW_WORKSPACE_ID"),
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
	if c.AuthEnabled() {
		origin, err := url.Parse(c.PublicOrigin)
		if err != nil || origin.Host == "" || (origin.Scheme != "http" && origin.Scheme != "https") || origin.User != nil || (origin.Path != "" && origin.Path != "/") || origin.RawQuery != "" || origin.Fragment != "" {
			return fmt.Errorf("CONTENTFLOW_PUBLIC_ORIGIN must be an HTTP or HTTPS origin")
		}
		if origin.Scheme == "http" && !loopbackHostname(origin.Hostname()) {
			return fmt.Errorf("authenticated HTTP origins are allowed only on loopback")
		}
	}
	if c.Environment == "production" {
		required := map[string]string{
			"CONTENTFLOW_GOOGLE_PROJECT_ID":   c.GoogleProject,
			"CONTENTFLOW_PUBLIC_ORIGIN":       c.PublicOrigin,
			"CONTENTFLOW_OAUTH_ISSUER":        c.OAuthIssuer,
			"CONTENTFLOW_OAUTH_CLIENT_ID":     c.OAuthClientID,
			"CONTENTFLOW_OAUTH_CLIENT_SECRET": c.OAuthSecret,
			"CONTENTFLOW_OWNER_SUBJECT":       c.OwnerSubject,
			"CONTENTFLOW_WORKSPACE_ID":        c.WorkspaceID,
		}
		for name, value := range required {
			if strings.TrimSpace(value) == "" {
				return fmt.Errorf("%s is required in production", name)
			}
		}
		origin, err := url.Parse(c.PublicOrigin)
		if err != nil || origin.Scheme != "https" || origin.Host == "" || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" {
			return fmt.Errorf("CONTENTFLOW_PUBLIC_ORIGIN must be an HTTPS origin in production")
		}
	}
	return nil
}

func loopbackHostname(hostname string) bool {
	if strings.EqualFold(hostname, "localhost") {
		return true
	}
	ip := net.ParseIP(hostname)
	return ip != nil && ip.IsLoopback()
}

func (c Config) AuthEnabled() bool {
	return c.PublicOrigin != "" && c.OAuthIssuer != "" && c.OAuthClientID != "" && c.OAuthSecret != "" && c.OwnerSubject != "" && c.WorkspaceID != "" && c.GoogleProject != ""
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
