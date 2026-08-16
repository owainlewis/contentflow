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
	Environment       string
	PublicAddress     string
	PrivateAddress    string
	AssetDirectory    string
	FirestoreHost     string
	LocalProxyAuth    bool
	LoopbackPortProxy bool
	GoogleProject     string
	PublicOrigin      string
	OAuthIssuer       string
	OAuthClientID     string
	OAuthSecret       string
	OwnerSubject      string
	WorkspaceID       string
}

func Load() (Config, error) {
	localProxyAuth, err := boolEnv("CONTENTFLOW_LOCAL_PROXY_AUTH", false)
	if err != nil {
		return Config{}, err
	}
	loopbackPortProxy, err := boolEnv("CONTENTFLOW_LOOPBACK_PORT_PROXY", false)
	if err != nil {
		return Config{}, err
	}

	cfg := Config{
		Environment:       stringEnv("CONTENTFLOW_ENV", "development"),
		PublicAddress:     stringEnv("CONTENTFLOW_ADDR", ":8080"),
		PrivateAddress:    stringEnv("CONTENTFLOW_PRIVATE_ADDR", ":8081"),
		AssetDirectory:    stringEnv("CONTENTFLOW_ASSET_DIR", "var/assets"),
		FirestoreHost:     os.Getenv("FIRESTORE_EMULATOR_HOST"),
		LocalProxyAuth:    localProxyAuth,
		LoopbackPortProxy: loopbackPortProxy,
		GoogleProject:     os.Getenv("CONTENTFLOW_GOOGLE_PROJECT_ID"),
		PublicOrigin:      os.Getenv("CONTENTFLOW_PUBLIC_ORIGIN"),
		OAuthIssuer:       os.Getenv("CONTENTFLOW_OAUTH_ISSUER"),
		OAuthClientID:     os.Getenv("CONTENTFLOW_OAUTH_CLIENT_ID"),
		OAuthSecret:       os.Getenv("CONTENTFLOW_OAUTH_CLIENT_SECRET"),
		OwnerSubject:      os.Getenv("CONTENTFLOW_OWNER_SUBJECT"),
		WorkspaceID:       os.Getenv("CONTENTFLOW_WORKSPACE_ID"),
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
	if c.Environment == "production" && c.FirestoreHost != "" {
		return fmt.Errorf("FIRESTORE_EMULATOR_HOST cannot be set in production")
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
	if c.LocalProxyAuth && strings.TrimSpace(c.FirestoreHost) == "" {
		return fmt.Errorf("FIRESTORE_EMULATOR_HOST is required when local proxy authentication is enabled")
	}
	if c.LocalProxyAuth && !loopbackAddress(c.PublicAddress) && !c.LoopbackPortProxy {
		return fmt.Errorf("CONTENTFLOW_ADDR must bind to loopback when local proxy authentication is enabled")
	}
	if c.LoopbackPortProxy && !c.LocalProxyAuth {
		return fmt.Errorf("CONTENTFLOW_LOOPBACK_PORT_PROXY requires local proxy authentication")
	}
	authValues := c.authenticationValues()
	configuredAuthValues := 0
	for _, value := range authValues {
		if strings.TrimSpace(value) != "" {
			configuredAuthValues++
		}
	}
	if configuredAuthValues != 0 && configuredAuthValues != len(authValues) {
		return fmt.Errorf("authentication configuration must be either complete or empty")
	}
	if c.AuthEnabled() {
		origin, err := url.Parse(c.PublicOrigin)
		if err != nil || origin.Host == "" || (origin.Scheme != "http" && origin.Scheme != "https") || origin.User != nil || (origin.Path != "" && origin.Path != "/") || origin.RawQuery != "" || origin.Fragment != "" {
			return fmt.Errorf("CONTENTFLOW_PUBLIC_ORIGIN must be an HTTP or HTTPS origin")
		}
		if origin.Scheme == "http" && !loopbackHostname(origin.Hostname()) {
			return fmt.Errorf("authenticated HTTP origins are allowed only on loopback")
		}
		issuer, err := url.Parse(c.OAuthIssuer)
		if err != nil || issuer.Host == "" || (issuer.Scheme != "http" && issuer.Scheme != "https") || issuer.User != nil || issuer.RawQuery != "" || issuer.Fragment != "" {
			return fmt.Errorf("CONTENTFLOW_OAUTH_ISSUER must be an HTTP or HTTPS URL")
		}
		if issuer.Scheme == "http" && !loopbackHostname(issuer.Hostname()) {
			return fmt.Errorf("plaintext OAuth issuers are allowed only on loopback")
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
		if err != nil || origin.Scheme != "https" || origin.Host == "" || (origin.Path != "" && origin.Path != "/") || origin.RawQuery != "" || origin.Fragment != "" {
			return fmt.Errorf("CONTENTFLOW_PUBLIC_ORIGIN must be an HTTPS origin in production")
		}
		issuer, err := url.Parse(c.OAuthIssuer)
		if err != nil || issuer.Scheme != "https" || issuer.Host == "" || issuer.User != nil || issuer.RawQuery != "" || issuer.Fragment != "" {
			return fmt.Errorf("CONTENTFLOW_OAUTH_ISSUER must use HTTPS in production")
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

func loopbackAddress(address string) bool {
	host, _, err := net.SplitHostPort(address)
	return err == nil && host != "" && loopbackHostname(host)
}

func (c Config) AuthEnabled() bool {
	for _, value := range c.authenticationValues() {
		if strings.TrimSpace(value) == "" {
			return false
		}
	}
	return true
}

func (c Config) authenticationValues() []string {
	return []string{c.GoogleProject, c.PublicOrigin, c.OAuthIssuer, c.OAuthClientID, c.OAuthSecret, c.OwnerSubject, c.WorkspaceID}
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
