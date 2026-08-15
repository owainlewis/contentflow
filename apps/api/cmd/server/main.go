package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/owainlewis/contentflow/apps/api/internal/auth"
	"github.com/owainlewis/contentflow/apps/api/internal/config"
	"github.com/owainlewis/contentflow/apps/api/internal/health"
	"github.com/owainlewis/contentflow/apps/api/internal/server"
	webassets "github.com/owainlewis/contentflow/apps/api/web"
)

const shutdownTimeout = 10 * time.Second

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		if err := checkHealth(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	cfg, err := config.Load()
	if err != nil {
		slog.Error("invalid configuration", "error", err)
		os.Exit(1)
	}
	if err := os.MkdirAll(cfg.AssetDirectory, 0o750); err != nil {
		slog.Error("create asset directory", "error", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, cfg); err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, cfg config.Config) error {
	checker := health.Dependencies{
		AssetDirectory: cfg.AssetDirectory,
		FirestoreHost:  cfg.FirestoreHost,
	}
	var authentication *auth.Service
	if cfg.AuthEnabled() {
		provider, err := auth.NewOIDCProvider(ctx, cfg.OAuthIssuer, cfg.OAuthClientID, cfg.OAuthSecret, cfg.PublicOrigin+"/api/v1/auth/callback")
		if err != nil {
			return err
		}
		firestoreClient, err := firestore.NewClient(ctx, cfg.GoogleProject)
		if err != nil {
			return fmt.Errorf("connect to Firestore auth store: %w", err)
		}
		defer firestoreClient.Close()
		credentialKey := sha256.Sum256([]byte(cfg.OAuthSecret))
		authentication, err = auth.New(auth.Config{
			PublicOrigin: cfg.PublicOrigin, OwnerIssuer: cfg.OAuthIssuer, OwnerSubject: cfg.OwnerSubject,
			WorkspaceID: cfg.WorkspaceID, SecureCookie: cfg.Environment == "production", CredentialKey: credentialKey[:],
		}, provider, auth.NewFirestoreStore(firestoreClient))
		if err != nil {
			return fmt.Errorf("configure authentication: %w", err)
		}
	}
	api := server.NewAPI(checker, authentication)
	servers := make([]*http.Server, 0, 2)

	publicHandler := server.NewApplication(webassets.Assets(), api)
	if cfg.LocalProxyAuth {
		secret, err := generateProxySecret()
		if err != nil {
			return fmt.Errorf("generate local proxy secret: %w", err)
		}
		privateServer := &http.Server{
			Addr:              cfg.PrivateAddress,
			Handler:           server.RequireProxySecret(api, secret),
			ReadHeaderTimeout: 5 * time.Second,
		}
		servers = append(servers, privateServer)

		privateURL, err := url.Parse("http://" + loopbackAddress(cfg.PrivateAddress))
		if err != nil {
			return fmt.Errorf("parse private API address: %w", err)
		}
		publicHandler = server.NewLocalPublicApplication(webassets.Assets(), privateURL, secret)
	}

	publicServer := &http.Server{
		Addr:              cfg.PublicAddress,
		Handler:           publicHandler,
		ReadHeaderTimeout: 5 * time.Second,
	}
	servers = append(servers, publicServer)

	errorChannel := make(chan error, len(servers))
	for _, httpServer := range servers {
		httpServer := httpServer
		go func() {
			slog.Info("ContentFlow listening", "address", httpServer.Addr)
			err := httpServer.ListenAndServe()
			if !errors.Is(err, http.ErrServerClosed) {
				errorChannel <- err
			}
		}()
	}

	var runErr error
	select {
	case <-ctx.Done():
	case runErr = <-errorChannel:
	}

	shutdownContext, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	for _, httpServer := range servers {
		if err := httpServer.Shutdown(shutdownContext); err != nil && runErr == nil {
			runErr = err
		}
	}
	return runErr
}

func generateProxySecret() (string, error) {
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return hex.EncodeToString(random), nil
}

func loopbackAddress(address string) string {
	if strings.HasPrefix(address, ":") {
		return "127.0.0.1" + address
	}
	host, port, err := net.SplitHostPort(address)
	if err == nil && (host == "" || host == "0.0.0.0" || host == "::") {
		return net.JoinHostPort("127.0.0.1", port)
	}
	return address
}

func checkHealth(arguments []string) error {
	endpoint := "http://127.0.0.1:8080/health/ready"
	if len(arguments) > 0 {
		endpoint = arguments[0]
	}
	client := http.Client{Timeout: 2 * time.Second}
	response, err := client.Get(endpoint)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("health endpoint returned %s", response.Status)
	}
	return nil
}
