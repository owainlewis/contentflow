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

func TestConfigurationRequiresADatabaseURL(t *testing.T) {
	cfg := productionConfig(t)
	cfg.DatabaseURL = ""
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected configuration without CONTENTFLOW_DATABASE_URL to be rejected")
	}
}

func productionConfig(t *testing.T) Config {
	t.Helper()
	return Config{
		Environment: "production", PublicAddress: ":8080", PrivateAddress: ":8081", AssetDirectory: t.TempDir(),
		DatabaseURL: "postgres://localhost/contentflow", PublicOrigin: "https://contentflow.example", OAuthIssuer: "https://accounts.google.com",
		OAuthClientID: "client", OAuthSecret: "secret", OwnerSubject: "owner", WorkspaceID: "workspace",
	}
}

func TestProductionRequiresCompleteAuthenticationConfiguration(t *testing.T) {
	fields := []struct {
		name  string
		clear func(*Config)
	}{
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
	cfg.PublicOrigin = "https://contentflow.example/"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("production origin with a trailing slash failed: %v", err)
	}
}

func TestProductionRequiresHTTPSOAuthIssuer(t *testing.T) {
	for _, issuer := range []string{"http://accounts.example", "http://localhost:5556/oidc"} {
		cfg := productionConfig(t)
		cfg.OAuthIssuer = issuer
		if err := cfg.Validate(); err == nil {
			t.Fatalf("expected plaintext production OAuth issuer %q to fail", issuer)
		}
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

func TestAuthenticatedNonProductionOAuthIssuerMustBeHTTPSOrLoopback(t *testing.T) {
	cfg := productionConfig(t)
	cfg.Environment = "staging"
	cfg.OAuthIssuer = "http://accounts.example"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected authenticated staging to reject a remote plaintext OAuth issuer")
	}

	cfg.Environment = "development"
	cfg.PublicOrigin = "http://localhost:3000"
	cfg.OAuthIssuer = "http://127.0.0.1:5556/oidc"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("explicit loopback HTTP OAuth issuer failed: %v", err)
	}
}

func TestNonProductionRejectsPartialAuthenticationConfiguration(t *testing.T) {
	fields := []struct {
		name  string
		clear func(*Config)
	}{
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
			cfg.Environment = "staging"
			field.clear(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatal("expected partial non-production authentication configuration to fail")
			}
		})
	}
}

func TestDevelopmentAcceptsLocalProxyAuthentication(t *testing.T) {
	t.Parallel()

	cfg := Config{
		Environment:    "development",
		PublicAddress:  "127.0.0.1:8080",
		PrivateAddress: ":8081",
		AssetDirectory: t.TempDir(),
		LocalProxyAuth: true,
		DatabaseURL:    "postgres://localhost/contentflow",
	}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected valid development configuration: %v", err)
	}
}

func TestDevelopmentRejectsWildcardLocalProxyAuthentication(t *testing.T) {
	t.Parallel()

	cfg := Config{
		Environment: "development", PublicAddress: ":8080", PrivateAddress: ":8081", AssetDirectory: t.TempDir(),
		LocalProxyAuth: true, DatabaseURL: "postgres://localhost/contentflow",
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected wildcard local proxy authentication address to fail")
	}
}

func TestContainerPortProxyMayAssertExternalLoopbackBinding(t *testing.T) {
	t.Parallel()

	cfg := Config{
		Environment: "development", PublicAddress: ":8080", PrivateAddress: ":8081", AssetDirectory: t.TempDir(),
		LocalProxyAuth: true, LoopbackPortProxy: true, DatabaseURL: "postgres://db/contentflow",
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("externally loopback-bound container proxy failed: %v", err)
	}
}

func TestDevelopmentRejectsLocalProxyAuthenticationWithoutADatabase(t *testing.T) {
	t.Parallel()

	cfg := Config{
		Environment:    "development",
		PublicAddress:  ":8080",
		PrivateAddress: ":8081",
		AssetDirectory: t.TempDir(),
		LocalProxyAuth: true,
	}

	if err := cfg.Validate(); err == nil {
		t.Fatal("expected configuration without a database URL to fail")
	}
}
