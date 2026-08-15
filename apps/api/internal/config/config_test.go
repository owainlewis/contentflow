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
