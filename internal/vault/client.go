package vault

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type TokenSource interface {
	Token() (string, error)
}

type SnapshotResult struct {
	Size   int64
	SHA256 string
}

type Client struct {
	baseURL    string
	httpClient *http.Client
	tokens     TokenSource
}

type StaticTokenSource struct {
	value string
}

type FileTokenSource struct {
	path string
}

type systemCertPoolResult struct {
	pool *x509.CertPool
	err  error
}

var loadSystemCertPool = func() systemCertPoolResult {
	pool, err := x509.SystemCertPool()
	return systemCertPoolResult{
		pool: pool,
		err:  err,
	}
}

func NewClient(baseURL string, timeout time.Duration, tokens TokenSource, caCertFile string) (*Client, error) {
	httpClient := &http.Client{
		Timeout: timeout,
	}
	if caCertFile != "" {
		tlsConfig, err := loadTLSConfig(caCertFile)
		if err != nil {
			return nil, err
		}
		httpClient.Transport = &http.Transport{
			TLSClientConfig: tlsConfig,
		}
	}
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		httpClient: httpClient,
		tokens:     tokens,
	}, nil
}

func NewTokenSource(staticToken string, tokenFile string) (TokenSource, error) {
	if strings.TrimSpace(tokenFile) != "" {
		return FileTokenSource{path: tokenFile}, nil
	}
	if strings.TrimSpace(staticToken) != "" {
		return StaticTokenSource{value: staticToken}, nil
	}
	return nil, errors.New("vault token source is required")
}

func loadTLSConfig(caCertFile string) (*tls.Config, error) {
	systemPool := loadSystemCertPool()
	if systemPool.err != nil {
		return nil, fmt.Errorf("load system cert pool: %w", systemPool.err)
	}
	rootCAs := systemPool.pool
	if rootCAs == nil {
		rootCAs = x509.NewCertPool()
	}
	//nolint:gosec // The CA bundle path comes from validated operator configuration.
	caCertPEM, err := os.ReadFile(caCertFile)
	if err != nil {
		return nil, fmt.Errorf("read vault ca cert file: %w", err)
	}
	if ok := rootCAs.AppendCertsFromPEM(caCertPEM); !ok {
		return nil, errors.New("parse vault ca cert file: no certificates found")
	}
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		RootCAs:    rootCAs,
	}, nil
}

func (s StaticTokenSource) Token() (string, error) {
	return strings.TrimSpace(s.value), nil
}

func (s FileTokenSource) Token() (string, error) {
	content, err := os.ReadFile(s.path)
	if err != nil {
		return "", fmt.Errorf("read vault token file: %w", err)
	}
	value := strings.TrimSpace(string(content))
	if value == "" {
		return "", errors.New("vault token file is empty")
	}
	return value, nil
}

func (c *Client) Snapshot(ctx context.Context, writer io.Writer) (SnapshotResult, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v1/sys/storage/raft/snapshot", http.NoBody)
	if err != nil {
		return SnapshotResult{}, fmt.Errorf("create snapshot request: %w", err)
	}
	token, err := c.tokens.Token()
	if err != nil {
		return SnapshotResult{}, err
	}
	request.Header.Set("X-Vault-Token", token)

	response, err := c.httpClient.Do(request)
	if err != nil {
		return SnapshotResult{}, fmt.Errorf("request snapshot: %w", err)
	}
	defer closeResponseBody(response)
	if response.StatusCode != http.StatusOK {
		body, readErr := io.ReadAll(io.LimitReader(response.Body, 4096))
		if readErr != nil {
			return SnapshotResult{}, fmt.Errorf("read vault snapshot error response: %w", readErr)
		}
		return SnapshotResult{}, fmt.Errorf("vault snapshot request failed with status %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
	}

	hasher := sha256.New()
	n, err := io.Copy(io.MultiWriter(writer, hasher), response.Body)
	if err != nil {
		return SnapshotResult{}, fmt.Errorf("stream snapshot response: %w", err)
	}
	return SnapshotResult{Size: n, SHA256: hex.EncodeToString(hasher.Sum(nil))}, nil
}

func (c *Client) Health(ctx context.Context) error {
	path := c.baseURL + "/v1/sys/health?standbyok=true&perfstandbyok=true"
	request, err := http.NewRequestWithContext(ctx, http.MethodHead, path, http.NoBody)
	if err != nil {
		return fmt.Errorf("create vault health request: %w", err)
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("request vault health: %w", err)
	}
	defer closeResponseBody(response)
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("vault health returned status %d", response.StatusCode)
	}
	return nil
}

func closeResponseBody(response *http.Response) {
	if response == nil || response.Body == nil {
		return
	}
	if err := response.Body.Close(); err != nil {
		return
	}
}

func SanitizeURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	return parsed.Redacted()
}
