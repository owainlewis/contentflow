package config

import "testing"

func TestProductionRejectsLocalProxyAuthentication(t *testing.T) {
	t.Parallel()

	cfg := Config{
		Environment:    "production",
		PublicAddress:  ":8080",
		PrivateAddress: ":8081",
		AssetDirectory: t.TempDir(),
		LocalProxyAuth: true,
	}

	if err := cfg.Validate(); err == nil {
		t.Fatal("expected production configuration to reject local proxy authentication")
	}
}

func productionConfig(t *testing.T) Config {
	t.Helper()
	return Config{
		Environment: "production", PublicAddress: ":8080", PrivateAddress: ":8081", AssetDirectory: t.TempDir(),
		GoogleProject: "project", PublicOrigin: "https://contentflow.example", OAuthIssuer: "https://accounts.google.com",
		OAuthClientID: "client", OAuthSecret: "secret", OwnerSubject: "owner", WorkspaceID: "workspace",
	}
}

func TestProductionRequiresCompleteAuthenticationConfiguration(t *testing.T) {
	fields := []struct {
		name  string
		clear func(*Config)
	}{
		{"project", func(c *Config) { c.GoogleProject = "" }},
		{"origin", func(c *Config) { c.PublicOrigin = "" }},
		{"issuer", func(c *Config) { c.OAuthIssuer = "" }},
		{"client id", func(c *Config) { c.OAuthClientID = "" }},
		{"client secret", func(c *Config) { c.OAuthSecret = "" }},
		{"owner", func(c *Config) { c.OwnerSubject = "" }},
		{"workspace", func(c *Config) { c.WorkspaceID = "" }},
	}
	for _, field := range fields {
		t.Run(field.name, func(t *testing.T) {
			cfg := productionConfig(t)
			field.clear(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatal("expected missing authentication configuration to fail")
			}
		})
	}
}

func TestProductionRequiresHTTPSPublicOrigin(t *testing.T) {
	cfg := productionConfig(t)
	cfg.PublicOrigin = "http://contentflow.example"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected insecure production origin to fail")
	}
}

func TestAuthenticatedNonProductionOriginMustBeHTTPSOrLoopback(t *testing.T) {
	cfg := productionConfig(t)
	cfg.Environment = "staging"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("authenticated HTTPS staging config failed: %v", err)
	}

	cfg.PublicOrigin = "http://staging.contentflow.example"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected authenticated non-loopback HTTP origin to fail")
	}

	cfg.Environment = "development"
	cfg.PublicOrigin = "http://127.0.0.1:3000"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("explicit loopback HTTP development origin failed: %v", err)
	}
}

func TestDevelopmentAcceptsLocalProxyAuthentication(t *testing.T) {
	t.Parallel()

	cfg := Config{
		Environment:    "development",
		PublicAddress:  ":8080",
		PrivateAddress: ":8081",
		AssetDirectory: t.TempDir(),
		LocalProxyAuth: true,
	}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected valid development configuration: %v", err)
	}
}
