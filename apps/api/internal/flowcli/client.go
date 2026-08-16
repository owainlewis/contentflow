package flowcli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	mutationAttempts     = 3
	maxResponseBytes     = 16 << 20
	maxListResponseBytes = 32 << 20
)

type apiResponse struct {
	Status int
	Body   []byte
}

type client struct {
	baseURL string
	token   string
	http    *http.Client
	sleep   func(context.Context, time.Duration) error
}

func newClient(baseURL, token string, httpClient *http.Client, sleep func(context.Context, time.Duration) error) (*client, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return nil, fmt.Errorf("CONTENTFLOW_API_URL must be an absolute origin")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return nil, fmt.Errorf("CONTENTFLOW_API_URL must not contain a path")
	}
	if parsed.Scheme != "https" && (parsed.Scheme != "http" || !isLoopback(parsed.Hostname())) {
		return nil, fmt.Errorf("CONTENTFLOW_API_URL must use HTTPS unless it is a loopback address")
	}
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("CONTENTFLOW_API_TOKEN is required")
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	configuredHTTPClient := *httpClient
	configuredHTTPClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	if sleep == nil {
		sleep = sleepContext
	}
	parsed.Path = ""
	return &client{baseURL: strings.TrimSuffix(parsed.String(), "/"), token: token, http: &configuredHTTPClient, sleep: sleep}, nil
}

func isLoopback(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (c *client) get(ctx context.Context, path string) (apiResponse, error) {
	return c.do(ctx, http.MethodGet, path, nil, false)
}

func (c *client) getStream(ctx context.Context, path string, consume func(io.Reader) error) (apiResponse, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return apiResponse{}, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+c.token)
	response, err := c.http.Do(request)
	if err != nil {
		return apiResponse{}, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode == http.StatusOK {
		limited := &io.LimitedReader{R: response.Body, N: maxListResponseBytes + 1}
		consumeErr := consume(limited)
		if limited.N <= 0 {
			return apiResponse{Status: response.StatusCode}, errors.New("list response exceeds 32 MiB")
		}
		return apiResponse{Status: response.StatusCode}, consumeErr
	}
	contents, readErr := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if readErr != nil {
		return apiResponse{}, readErr
	}
	if len(contents) > maxResponseBytes {
		return apiResponse{}, errors.New("response exceeds 16 MiB")
	}
	return apiResponse{Status: response.StatusCode, Body: contents}, nil
}

func (c *client) mutate(ctx context.Context, method, path string, body []byte) (apiResponse, error) {
	return c.do(ctx, method, path, body, true)
}

func (c *client) do(ctx context.Context, method, path string, body []byte, retryTimeout bool) (apiResponse, error) {
	attempts := 1
	if retryTimeout {
		attempts = mutationAttempts
	}
	for attempt := 0; attempt < attempts; attempt++ {
		request, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bytes.NewReader(body))
		if err != nil {
			return apiResponse{}, err
		}
		request.Header.Set("Accept", "application/json")
		request.Header.Set("Authorization", "Bearer "+c.token)
		if body != nil {
			request.Header.Set("Content-Type", "application/json")
		}
		response, err := c.http.Do(request)
		if err == nil {
			contents, readErr := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
			_ = response.Body.Close()
			if readErr != nil {
				if retryTimeout && isTimeout(readErr) && attempt < attempts-1 && ctx.Err() == nil {
					if err := c.sleep(ctx, time.Duration(attempt+1)*100*time.Millisecond); err != nil {
						return apiResponse{}, err
					}
					continue
				}
				return apiResponse{}, readErr
			}
			if len(contents) > maxResponseBytes {
				return apiResponse{}, errors.New("response exceeds 16 MiB")
			}
			return apiResponse{Status: response.StatusCode, Body: contents}, nil
		}
		if !retryTimeout || !isTimeout(err) || attempt == attempts-1 || ctx.Err() != nil {
			return apiResponse{}, err
		}
		if err := c.sleep(ctx, time.Duration(attempt+1)*100*time.Millisecond); err != nil {
			return apiResponse{}, err
		}
	}
	return apiResponse{}, errors.New("request failed")
}

func isTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var networkError net.Error
	return errors.As(err, &networkError) && networkError.Timeout()
}

func sleepContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
