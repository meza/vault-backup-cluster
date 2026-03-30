package consulx

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	consulapi "github.com/hashicorp/consul/api"
)

type fakeStatus struct {
	leader string
	err    error
}

func (f fakeStatus) Leader() (string, error) {
	return f.leader, f.err
}

type fakeLockClient struct {
	lock *fakeLock
	err  error
	opts *consulapi.LockOptions
}

func (f *fakeLockClient) LockOpts(opts *consulapi.LockOptions) (lockHandle, error) {
	f.opts = opts
	if f.err != nil {
		return nil, f.err
	}
	return f.lock, nil
}

type fakeLock struct {
	leadershipLost <-chan struct{}
	lockErr        error
	unlockErr      error
	unlockCalls    int
}

func (f *fakeLock) Lock(<-chan struct{}) (<-chan struct{}, error) {
	return f.leadershipLost, f.lockErr
}

func (f *fakeLock) Unlock() error {
	f.unlockCalls++
	return f.unlockErr
}

type fakeTokenSource struct {
	value string
	err   error
}

func (f fakeTokenSource) Token() (string, error) {
	return f.value, f.err
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestCheckStatusRejectsEmptyLeader(t *testing.T) {
	err := checkStatus(context.Background(), fakeStatus{})
	if err == nil || !strings.Contains(err.Error(), "empty leader") {
		t.Fatalf("expected empty leader error, got %v", err)
	}
}

func TestCheckStatusWrapsLeaderLookupError(t *testing.T) {
	err := checkStatus(context.Background(), fakeStatus{err: errors.New("boom")})
	if err == nil || !strings.Contains(err.Error(), "query consul leader") {
		t.Fatalf("expected wrapped leader lookup error, got %v", err)
	}
}

func TestCheckStatusReturnsContextCancellationAfterLeaderLookup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := checkStatus(ctx, fakeStatus{leader: "10.0.0.1:8300"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
}

func TestNewClientAndCheckUseTokenTransport(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if got := request.Header.Get("X-Consul-Token"); got != "token-value" {
			t.Fatalf("expected token header, got %q", got)
		}
		if request.URL.Path != "/v1/status/leader" {
			t.Fatalf("expected leader path, got %s", request.URL.Path)
		}
		writer.Header().Set("Content-Type", "application/json")
		if _, err := writer.Write([]byte(`"10.0.0.1:8300"`)); err != nil {
			t.Fatalf("write leader response: %v", err)
		}
	}))
	defer server.Close()

	client, err := NewClient(server.URL, fakeTokenSource{value: " token-value "})
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}
	if err := Check(context.Background(), client); err != nil {
		t.Fatalf("Check returned error: %v", err)
	}
}

func TestNewClientSetsTimeoutWithoutTokens(t *testing.T) {
	client, err := NewClient("http://127.0.0.1:8500", nil)
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}

	timeout := reflect.ValueOf(client).Elem().FieldByName("config").FieldByName("HttpClient").Elem().FieldByName("Timeout").Int()
	if timeout <= 0 {
		t.Fatalf("expected configured timeout, got %d", timeout)
	}
}

func TestNewClientReturnsHTTPClientConstructionError(t *testing.T) {
	original := newHTTPClient
	newHTTPClient = func(*http.Transport, consulapi.TLSConfig) (*http.Client, error) {
		return nil, errors.New("boom")
	}
	defer func() {
		newHTTPClient = original
	}()

	_, err := NewClient("http://127.0.0.1:8500", nil)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected wrapped HTTP client error, got %v", err)
	}
}

func TestTokenTransportSetsTrimmedConsulToken(t *testing.T) {
	transport := tokenTransport{
		base: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			if got := request.Header.Get("X-Consul-Token"); got != "token-value" {
				t.Fatalf("expected trimmed token header, got %q", got)
			}
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("ok"))}, nil
		}),
		tokens: fakeTokenSource{value: " token-value \n"},
	}

	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://consul.local/v1/status/leader", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	response, err := transport.RoundTrip(request)
	if err != nil {
		t.Fatalf("RoundTrip returned error: %v", err)
	}
	DrainBody(response)
}

func TestTokenTransportReturnsTokenError(t *testing.T) {
	transport := tokenTransport{
		base: roundTripFunc(func(*http.Request) (*http.Response, error) {
			t.Fatal("base transport should not be called when token lookup fails")
			return nil, errors.New("unexpected round trip")
		}),
		tokens: fakeTokenSource{err: errors.New("boom")},
	}

	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://consul.local/v1/status/leader", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	response, err := transport.RoundTrip(request)
	if response != nil {
		DrainBody(response)
	}
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected token lookup error, got %v", err)
	}
}

func TestElectorRunCancelsLeadershipContextAndUnlocks(t *testing.T) {
	lost := make(chan struct{})
	lock := &fakeLock{leadershipLost: lost}
	client := &fakeLockClient{lock: lock}
	elector := buildElector(client, "service/vault-backup/leader", "node-a", 15*time.Second, 10*time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	callbacks := 0
	err := elector.Run(ctx, func(leaderCtx context.Context) error {
		callbacks++
		close(lost)
		<-leaderCtx.Done()
		cancel()
		return context.Canceled
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if callbacks != 1 {
		t.Fatalf("expected one callback invocation, got %d", callbacks)
	}
	if lock.unlockCalls != 1 {
		t.Fatalf("expected one unlock call, got %d", lock.unlockCalls)
	}
	if client.opts == nil || client.opts.Key != "service/vault-backup/leader" || client.opts.SessionTTL != "15s" {
		t.Fatalf("expected lock options to be recorded, got %#v", client.opts)
	}
}

func TestNewElectorUsesConsulLockClientWrapper(t *testing.T) {
	client, err := NewClient("http://127.0.0.1:8500", nil)
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}

	elector := NewElector(client, "lock", "node-a", 15*time.Second, 10*time.Second)
	lock, err := elector.client.LockOpts(&consulapi.LockOptions{Key: "lock"})
	if err != nil {
		t.Fatalf("LockOpts returned error: %v", err)
	}
	if lock == nil {
		t.Fatal("expected wrapped lock handle")
	}
}

func TestElectorRunReturnsCreateLockError(t *testing.T) {
	elector := buildElector(&fakeLockClient{err: errors.New("boom")}, "lock", "node-a", 15*time.Second, 10*time.Second)

	err := elector.Run(context.Background(), func(context.Context) error {
		t.Fatal("callback should not be called when lock creation fails")
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "create consul lock") {
		t.Fatalf("expected wrapped lock creation error, got %v", err)
	}
}

func TestElectorRunReturnsAcquireError(t *testing.T) {
	elector := buildElector(&fakeLockClient{lock: &fakeLock{lockErr: errors.New("boom")}}, "lock", "node-a", 15*time.Second, 10*time.Second)

	err := elector.Run(context.Background(), func(context.Context) error {
		t.Fatal("callback should not be called when lock acquisition fails")
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "acquire consul lock") {
		t.Fatalf("expected wrapped lock acquisition error, got %v", err)
	}
}

func TestElectorRunReturnsCallbackError(t *testing.T) {
	lost := make(chan struct{})
	lock := &fakeLock{leadershipLost: lost}
	client := &fakeLockClient{lock: lock}
	elector := buildElector(client, "lock", "node-a", 15*time.Second, 10*time.Second)
	callbackErr := errors.New("boom")

	err := elector.Run(context.Background(), func(context.Context) error {
		return callbackErr
	})
	if !errors.Is(err, callbackErr) {
		t.Fatalf("expected callback error, got %v", err)
	}
	if lock.unlockCalls != 1 {
		t.Fatalf("expected one unlock call, got %d", lock.unlockCalls)
	}
}

func TestElectorRunReturnsNilWhenLockStopsWithoutLeadershipChannel(t *testing.T) {
	elector := buildElector(&fakeLockClient{lock: &fakeLock{}}, "lock", "node-a", 15*time.Second, 10*time.Second)

	err := elector.Run(context.Background(), func(context.Context) error {
		t.Fatal("callback should not be called when no leadership channel is returned")
		return nil
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

func TestElectorRunIgnoresLockNotHeldOnUnlock(t *testing.T) {
	lost := make(chan struct{})
	lock := &fakeLock{leadershipLost: lost, unlockErr: consulapi.ErrLockNotHeld}
	elector := buildElector(&fakeLockClient{lock: lock}, "lock", "node-a", 15*time.Second, 10*time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := elector.Run(ctx, func(leaderCtx context.Context) error {
		close(lost)
		<-leaderCtx.Done()
		cancel()
		return context.Canceled
	})
	if err != nil {
		t.Fatalf("expected nil error when unlock reports lock not held, got %v", err)
	}
}

func TestElectorRunReturnsUnlockError(t *testing.T) {
	lost := make(chan struct{})
	lock := &fakeLock{leadershipLost: lost, unlockErr: errors.New("unlock failed")}
	elector := buildElector(&fakeLockClient{lock: lock}, "lock", "node-a", 15*time.Second, 10*time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := elector.Run(ctx, func(leaderCtx context.Context) error {
		close(lost)
		<-leaderCtx.Done()
		cancel()
		return context.Canceled
	})
	if err == nil || !strings.Contains(err.Error(), "release consul lock") {
		t.Fatalf("expected wrapped unlock error, got %v", err)
	}
}

func TestDrainBodyClosesBody(t *testing.T) {
	reader := &trackedReadCloser{ReadCloser: io.NopCloser(strings.NewReader("payload"))}

	DrainBody(&http.Response{Body: reader})

	if !reader.closed {
		t.Fatal("expected response body to be closed")
	}
}

func TestDrainBodyHandlesNilInputs(t *testing.T) {
	DrainBody(nil)
	DrainBody(&http.Response{})
}

func TestDrainBodyIgnoresReadAndCloseErrors(t *testing.T) {
	DrainBody(&http.Response{Body: &errReadCloser{closeErr: errors.New("close boom")}})
	DrainBody(&http.Response{Body: &trackedReadCloser{ReadCloser: io.NopCloser(strings.NewReader("payload")), closeErr: errors.New("close boom")}})
}

type trackedReadCloser struct {
	io.ReadCloser
	closed bool
	closeErr error
}

func (t *trackedReadCloser) Close() error {
	t.closed = true
	if t.closeErr != nil {
		return t.closeErr
	}
	return t.ReadCloser.Close()
}

type errReadCloser struct {
	closeErr error
}

func (e *errReadCloser) Read([]byte) (int, error) {
	return 0, errors.New("boom")
}

func (e *errReadCloser) Close() error {
	return e.closeErr
}
