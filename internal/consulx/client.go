package consulx

import (
	"context"
	"fmt"
	"io"
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

func NewClient(address string, tokens TokenSource) (*consulapi.Client, error) {
	config := consulapi.DefaultConfig()
	config.Address = address
	if tokens != nil {
		config.HttpClient = &http.Client{
			Transport: tokenTransport{base: http.DefaultTransport, tokens: tokens},
			Timeout:   30 * time.Second,
		}
	}
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
		return fmt.Errorf("consul returned an empty leader value")
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
}

func NewElector(client *consulapi.Client, lockKey string, nodeID string, sessionTTL time.Duration, lockWait time.Duration) *Elector {
	return newElector(consulLockClient{client: client}, lockKey, nodeID, sessionTTL, lockWait)
}

func newElector(client lockClient, lockKey string, nodeID string, sessionTTL time.Duration, lockWait time.Duration) *Elector {
	return &Elector{client: client, lockKey: lockKey, nodeID: nodeID, sessionTTL: sessionTTL, lockWait: lockWait}
}

func (e *Elector) Run(ctx context.Context, onLeadership func(context.Context) error) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
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
		leaderCtx, cancel := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() {
			defer close(done)
			select {
			case <-leadershipLost:
				cancel()
			case <-leaderCtx.Done():
			}
		}()
		callbackErr := onLeadership(leaderCtx)
		cancel()
		<-done
		if err := lock.Unlock(); err != nil && err != consulapi.ErrLockNotHeld {
			return fmt.Errorf("release consul lock: %w", err)
		}
		if callbackErr != nil && callbackErr != context.Canceled {
			return callbackErr
		}
	}
}

func (c consulLockClient) LockOpts(opts *consulapi.LockOptions) (lockHandle, error) {
	return c.client.LockOpts(opts)
}

func DrainBody(response *http.Response) {
	if response == nil || response.Body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
}
