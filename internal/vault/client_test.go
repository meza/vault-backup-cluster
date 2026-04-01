package vault

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type errorTokenSource struct {
	err error
}

func (e errorTokenSource) Token() (string, error) {
	return "", e.err
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, errors.New("boom")
}

func TestSnapshotReadsTokenFileForEachRequest(t *testing.T) {
	tokenPath := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenPath, []byte("first-token"), 0o600); err != nil {
		t.Fatalf("write token file: %v", err)
	}

	calls := make([]string, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Header.Get("X-Vault-Token"))
		if _, err := w.Write([]byte("snapshot")); err != nil {
			t.Fatalf("write snapshot response: %v", err)
		}
	}))
	defer server.Close()

	source, err := NewTokenSource("", tokenPath)
	if err != nil {
		t.Fatalf("NewTokenSource returned error: %v", err)
	}
	client, err := NewClient(server.URL, time.Minute, source, "")
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}

	if _, err := client.Snapshot(context.Background(), io.Discard); err != nil {
		t.Fatalf("first Snapshot returned error: %v", err)
	}
	if err := os.WriteFile(tokenPath, []byte("second-token"), 0o600); err != nil {
		t.Fatalf("rewrite token file: %v", err)
	}
	if _, err := client.Snapshot(context.Background(), io.Discard); err != nil {
		t.Fatalf("second Snapshot returned error: %v", err)
	}

	if len(calls) != 2 {
		t.Fatalf("expected two requests, got %d", len(calls))
	}
	if calls[0] != "first-token" || calls[1] != "second-token" {
		t.Fatalf("expected rotated tokens, got %#v", calls)
	}
}

func TestHealthAcceptsHealthyVault(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client, err := NewClient(server.URL, time.Minute, StaticTokenSource{value: "token"}, "")
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}
	if err := client.Health(context.Background()); err != nil {
		t.Fatalf("Health returned error: %v", err)
	}
}

func TestHealthUsesCustomCACertFile(t *testing.T) {
	caCertPEM, serverTLSCert := generateTestTLSMaterials(t)
	caPath := filepath.Join(t.TempDir(), "vault-ca.crt")
	if err := os.WriteFile(caPath, caCertPEM, 0o600); err != nil {
		t.Fatalf("write ca cert file: %v", err)
	}

	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	server.TLS = &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{serverTLSCert},
	}
	server.StartTLS()
	defer server.Close()

	client, err := NewClient(server.URL, time.Minute, StaticTokenSource{value: "token"}, caPath)
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}
	if err := client.Health(context.Background()); err != nil {
		t.Fatalf("Health returned error: %v", err)
	}
}

func TestNewClientReturnsCACertErrors(t *testing.T) {
	_, err := NewClient("https://vault.local", time.Minute, StaticTokenSource{value: "token"}, filepath.Join(t.TempDir(), "missing.crt"))
	if err == nil || !strings.Contains(err.Error(), "read vault ca cert file") {
		t.Fatalf("expected missing ca cert error, got %v", err)
	}

	caPath := filepath.Join(t.TempDir(), "invalid.crt")
	if writeErr := os.WriteFile(caPath, []byte("not-a-certificate"), 0o600); writeErr != nil {
		t.Fatalf("write invalid ca cert file: %v", writeErr)
	}
	_, err = NewClient("https://vault.local", time.Minute, StaticTokenSource{value: "token"}, caPath)
	if err == nil || !strings.Contains(err.Error(), "parse vault ca cert file") {
		t.Fatalf("expected parse ca cert error, got %v", err)
	}
}

func TestNewTokenSource(t *testing.T) {
	source, err := NewTokenSource(" static-token ", "")
	if err != nil {
		t.Fatalf("NewTokenSource returned error: %v", err)
	}
	token, err := source.Token()
	if err != nil || token != "static-token" {
		t.Fatalf("expected trimmed static token, got %q and %v", token, err)
	}

	filePath := filepath.Join(t.TempDir(), "token")
	if writeErr := os.WriteFile(filePath, []byte("file-token"), 0o600); writeErr != nil {
		t.Fatalf("write token file: %v", writeErr)
	}
	source, err = NewTokenSource("", filePath)
	if err != nil {
		t.Fatalf("NewTokenSource returned error: %v", err)
	}
	token, err = source.Token()
	if err != nil || token != "file-token" {
		t.Fatalf("expected file token, got %q and %v", token, err)
	}

	if _, err := NewTokenSource("", ""); err == nil || !strings.Contains(err.Error(), "vault token source is required") {
		t.Fatalf("expected missing token source error, got %v", err)
	}
}

func TestFileTokenSourceErrors(t *testing.T) {
	if _, err := (FileTokenSource{path: filepath.Join(t.TempDir(), "missing")}).Token(); err == nil || !strings.Contains(err.Error(), "read vault token file") {
		t.Fatalf("expected read error, got %v", err)
	}

	path := filepath.Join(t.TempDir(), "empty")
	if err := os.WriteFile(path, []byte(" \n "), 0o600); err != nil {
		t.Fatalf("write empty token file: %v", err)
	}
	if _, err := (FileTokenSource{path: path}).Token(); err == nil || !strings.Contains(err.Error(), "vault token file is empty") {
		t.Fatalf("expected empty token error, got %v", err)
	}
}

func TestSnapshotErrorPaths(t *testing.T) {
	client, err := NewClient("://bad", time.Minute, StaticTokenSource{value: "token"}, "")
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}
	if _, err := client.Snapshot(context.Background(), io.Discard); err == nil || !strings.Contains(err.Error(), "create snapshot request") {
		t.Fatalf("expected request creation error, got %v", err)
	}

	client, err = NewClient("http://vault.local", time.Minute, errorTokenSource{err: errors.New("boom")}, "")
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}
	if _, err := client.Snapshot(context.Background(), io.Discard); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected token error, got %v", err)
	}

	client, err = NewClient("http://vault.local", time.Minute, StaticTokenSource{value: "token"}, "")
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}
	client.httpClient.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("boom")
	})
	if _, err := client.Snapshot(context.Background(), io.Discard); err == nil || !strings.Contains(err.Error(), "request snapshot") {
		t.Fatalf("expected request error, got %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		if _, err := w.Write([]byte("backend failed")); err != nil {
			t.Fatalf("write backend failure response: %v", err)
		}
	}))
	defer server.Close()

	client, err = NewClient(server.URL, time.Minute, StaticTokenSource{value: "token"}, "")
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}
	if _, err := client.Snapshot(context.Background(), io.Discard); err == nil || !strings.Contains(err.Error(), "status 502") {
		t.Fatalf("expected non-200 error, got %v", err)
	}

	client, err = NewClient("http://vault.local", time.Minute, StaticTokenSource{value: "token"}, "")
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}
	client.httpClient.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusBadGateway,
			Body:       io.NopCloser(failingReader{}),
		}, nil
	})
	if _, err := client.Snapshot(context.Background(), io.Discard); err == nil || !strings.Contains(err.Error(), "read vault snapshot error response") {
		t.Fatalf("expected read error, got %v", err)
	}

	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := w.Write([]byte("snapshot")); err != nil {
			t.Fatalf("write snapshot response: %v", err)
		}
	}))
	defer server.Close()

	client, err = NewClient(server.URL, time.Minute, StaticTokenSource{value: "token"}, "")
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}
	if _, err := client.Snapshot(context.Background(), failingWriter{}); err == nil || !strings.Contains(err.Error(), "stream snapshot response") {
		t.Fatalf("expected writer error, got %v", err)
	}
}

func TestHealthErrorPaths(t *testing.T) {
	client, err := NewClient("://bad", time.Minute, StaticTokenSource{value: "token"}, "")
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}
	if err := client.Health(context.Background()); err == nil || !strings.Contains(err.Error(), "create vault health request") {
		t.Fatalf("expected request creation error, got %v", err)
	}

	client, err = NewClient("http://vault.local", time.Minute, StaticTokenSource{value: "token"}, "")
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}
	client.httpClient.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("boom")
	})
	if err := client.Health(context.Background()); err == nil || !strings.Contains(err.Error(), "request vault health") {
		t.Fatalf("expected request error, got %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	client, err = NewClient(server.URL, time.Minute, StaticTokenSource{value: "token"}, "")
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}
	if err := client.Health(context.Background()); err == nil || !strings.Contains(err.Error(), "status 429") {
		t.Fatalf("expected status error, got %v", err)
	}
}

func TestSanitizeURL(t *testing.T) {
	if got := SanitizeURL("http://user:secret@vault.local"); !strings.Contains(got, "xxxxx") {
		t.Fatalf("expected redacted url, got %q", got)
	}
	if got := SanitizeURL("://bad"); got != "://bad" {
		t.Fatalf("expected invalid URL to round trip, got %q", got)
	}
}

func TestCloseResponseBodyHandlesNilAndCloseErrors(t *testing.T) {
	closeResponseBody(nil)
	closeResponseBody(&http.Response{})
	closeResponseBody(&http.Response{Body: failingCloseReadCloser{ReadCloser: io.NopCloser(strings.NewReader("payload"))}})
}

func generateTestTLSMaterials(t *testing.T) ([]byte, tls.Certificate) {
	t.Helper()

	caPrivateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate ca key: %v", err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			CommonName: "vault-test-ca",
		},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caPrivateKey.PublicKey, caPrivateKey)
	if err != nil {
		t.Fatalf("create ca certificate: %v", err)
	}

	serverPrivateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate server key: %v", err)
	}
	serverTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject: pkix.Name{
			CommonName: "127.0.0.1",
		},
		NotBefore:   time.Now().Add(-time.Hour),
		NotAfter:    time.Now().Add(time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:    []string{"localhost"},
	}
	serverDER, err := x509.CreateCertificate(rand.Reader, serverTemplate, caTemplate, &serverPrivateKey.PublicKey, caPrivateKey)
	if err != nil {
		t.Fatalf("create server certificate: %v", err)
	}

	caCertPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	serverCertPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: serverDER})
	serverKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(serverPrivateKey)})

	serverTLSCert, err := tls.X509KeyPair(serverCertPEM, serverKeyPEM)
	if err != nil {
		t.Fatalf("load server key pair: %v", err)
	}

	return caCertPEM, serverTLSCert
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) {
	return 0, errors.New("boom")
}

type failingCloseReadCloser struct {
	io.ReadCloser
}

func (failingCloseReadCloser) Close() error {
	return errors.New("boom")
}
