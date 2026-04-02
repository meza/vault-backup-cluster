package consulx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	consulapi "github.com/hashicorp/consul/api"
)

type TokenSource interface {
	Token() (string, error)
}

type tokenTransport struct {
	base   http.RoundTripper
	tokens TokenSource
}

type leaderStatus interface {
	Leader() (string, error)
}

type lockHandle interface {
	Lock(stopCh <-chan struct{}) (<-chan struct{}, error)
	Unlock() error
}

type lockClient interface {
	LockOpts(opts *consulapi.LockOptions) (lockHandle, error)
}

type consulLockClient struct {
	client *consulapi.Client
}

const requestTimeout = 30 * time.Second

var newHTTPClient = consulapi.NewHttpClient

func NewClient(address string, tokens TokenSource) (*consulapi.Client, error) {
	config := consulapi.DefaultConfig()
	config.Address = address
	httpClient, err := newHTTPClient(config.Transport, config.TLSConfig)
	if err != nil {
		return nil, err
	}
	httpClient.Timeout = requestTimeout
	if tokens != nil {
		httpClient.Transport = tokenTransport{base: httpClient.Transport, tokens: tokens}
	}
	config.HttpClient = httpClient
	return consulapi.NewClient(config)
}

func (t tokenTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	cloned := request.Clone(request.Context())
	if t.tokens != nil {
		token, err := t.tokens.Token()
		if err != nil {
			return nil, err
		}
		cloned.Header.Set("X-Consul-Token", strings.TrimSpace(token))
	}
	return t.base.RoundTrip(cloned)
}

func Check(ctx context.Context, client *consulapi.Client) error {
	return checkStatus(ctx, client.Status())
}

func checkStatus(ctx context.Context, status leaderStatus) error {
	leader, err := status.Leader()
	if err != nil {
		return fmt.Errorf("query consul leader: %w", err)
	}
	if strings.TrimSpace(leader) == "" {
		return errors.New("consul returned an empty leader value")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	return nil
}

type Elector struct {
	client     lockClient
	lockKey    string
	nodeID     string
	sessionTTL time.Duration
	lockWait   time.Duration
	logger     *slog.Logger
}

func NewElector(client *consulapi.Client, lockKey string, nodeID string, sessionTTL time.Duration, lockWait time.Duration, logger *slog.Logger) *Elector {
	elector := buildElector(consulLockClient{client: client}, lockKey, nodeID, sessionTTL, lockWait)
	elector.logger = logger
	return elector
}

func buildElector(client lockClient, lockKey string, nodeID string, sessionTTL time.Duration, lockWait time.Duration) *Elector {
	return &Elector{client: client, lockKey: lockKey, nodeID: nodeID, sessionTTL: sessionTTL, lockWait: lockWait}
}

func (e *Elector) Run(ctx context.Context, onLeadership func(context.Context) error) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		e.logDebug("attempting consul leadership acquisition")
		lock, err := e.client.LockOpts(&consulapi.LockOptions{
			Key:          e.lockKey,
			Value:        []byte(e.nodeID),
			SessionName:  "vault-backup-cluster",
			SessionTTL:   e.sessionTTL.String(),
			LockWaitTime: e.lockWait,
		})
		if err != nil {
			return fmt.Errorf("create consul lock: %w", err)
		}
		leadershipLost, err := lock.Lock(ctx.Done())
		if err != nil {
			return fmt.Errorf("acquire consul lock: %w", err)
		}
		if leadershipLost == nil {
			return nil
		}
		e.logDebug("consul leadership acquired")
		leaderCtx, cancel := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() {
			defer close(done)
			select {
			case <-leadershipLost:
				e.logDebug("consul leadership lost")
				cancel()
			case <-leaderCtx.Done():
			}
		}()
		callbackErr := onLeadership(leaderCtx)
		cancel()
		<-done
		if err := lock.Unlock(); err != nil && !errors.Is(err, consulapi.ErrLockNotHeld) {
			return fmt.Errorf("release consul lock: %w", err)
		}
		e.logDebug("consul lock released")
		if callbackErr != nil && !errors.Is(callbackErr, context.Canceled) {
			return callbackErr
		}
	}
}

func (e *Elector) logDebug(message string) {
	if e.logger == nil {
		return
	}
	e.logger.Debug(message)
}

func (c consulLockClient) LockOpts(opts *consulapi.LockOptions) (lockHandle, error) {
	return c.client.LockOpts(opts)
}

func DrainBody(response *http.Response) {
	if response == nil || response.Body == nil {
		return
	}
	if _, err := io.Copy(io.Discard, response.Body); err != nil {
		_ = err
	}
	if err := response.Body.Close(); err != nil {
		_ = err
	}
}
