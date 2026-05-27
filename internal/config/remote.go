package config

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

// TokenSource is satisfied by identity.TokenSource.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
	// Invalidate clears any cached token. Called by Fetcher when the config
	// service responds with 401, so the next Token() call fetches a fresh one
	// rather than replaying the rejected credential for its full TTL.
	Invalidate()
}

// NamespaceStatus records the outcome of the most recent fetch attempt for
// a single config namespace.
type NamespaceStatus struct {
	OK        bool      `json:"ok"`
	FetchedAt time.Time `json:"fetched_at"`
	Error     string    `json:"error,omitempty"`
}

// Fetcher fetches the three statehouse config namespaces from the remote
// config service and merges them into a local Config. Errors are per-namespace
// and non-fatal: a namespace that cannot be fetched is skipped with a warning
// and the local config value for that section is preserved.
type Fetcher struct {
	BaseURL    string
	Tokens     TokenSource
	HTTPClient *http.Client
	Logger     *slog.Logger

	mu       sync.RWMutex
	statuses map[string]NamespaceStatus
}

// defaultFetchClient is used when Fetcher.HTTPClient is nil.
var defaultFetchClient = &http.Client{Timeout: 10 * time.Second}

// maxConfigBytes is the maximum response body size accepted from the config service.
const maxConfigBytes = 1 << 20 // 1 MiB

// Statuses returns a snapshot of the last fetch result for each namespace.
func (f *Fetcher) Statuses() map[string]NamespaceStatus {
	f.mu.RLock()
	defer f.mu.RUnlock()
	out := make(map[string]NamespaceStatus, len(f.statuses))
	for k, v := range f.statuses {
		out[k] = v
	}
	return out
}

// behaviourDoc mirrors the JSON structure of the statehouse_behaviour
// namespace. Pointer fields let us distinguish "key absent" (nil — keep
// local) from "key present but empty" (non-nil — replace local).
type behaviourDoc struct {
	Energy       *EnergyConfig       `json:"energy,omitempty"`
	Availability *AvailabilityConfig `json:"availability,omitempty"`
	House        *HouseConfig        `json:"house,omitempty"`
	Adapters     *AdaptersConfig     `json:"adapters,omitempty"`
}

// ApplyRemote fetches the three statehouse namespaces and merges them into
// cfg. Remote values win over local on overlap. Each namespace is fetched
// independently; a failure on one does not prevent the others from applying.
func (f *Fetcher) ApplyRemote(ctx context.Context, cfg *Config) {
	if !strings.HasPrefix(f.BaseURL, "https://") {
		f.warn("remote config: base_url is not https — bearer token will be transmitted in cleartext", "url", f.BaseURL)
	}
	token, err := f.Tokens.Token(ctx)
	if err != nil {
		f.warn("remote config: identity token fetch failed, using local config only", "error", err)
		return
	}
	f.applyClasses(ctx, cfg, token)
	f.applyDevices(ctx, cfg, token)
	f.applyBehaviour(ctx, cfg, token)
}

func (f *Fetcher) applyClasses(ctx context.Context, cfg *Config, token string) {
	var remote map[string]DeviceClassConfig
	if err := f.fetch(ctx, token, "statehouse_classes", &remote); err != nil {
		f.warn("remote config: statehouse_classes unavailable, using local", "error", err)
		f.recordStatus("statehouse_classes", err)
		return
	}
	f.recordStatus("statehouse_classes", nil)
	if cfg.DeviceClasses == nil {
		cfg.DeviceClasses = make(map[string]DeviceClassConfig)
	}
	for k, v := range remote {
		cfg.DeviceClasses[k] = v
	}
}

func (f *Fetcher) applyDevices(ctx context.Context, cfg *Config, token string) {
	var remote map[string]DeviceConfig
	if err := f.fetch(ctx, token, "statehouse_devices", &remote); err != nil {
		f.warn("remote config: statehouse_devices unavailable, using local", "error", err)
		f.recordStatus("statehouse_devices", err)
		return
	}
	f.recordStatus("statehouse_devices", nil)
	if cfg.Devices == nil {
		cfg.Devices = make(map[string]DeviceConfig)
	}
	for k, v := range remote {
		cfg.Devices[k] = v
	}
	normaliseDevices(cfg.Devices)
}

func (f *Fetcher) applyBehaviour(ctx context.Context, cfg *Config, token string) {
	var b behaviourDoc
	if err := f.fetch(ctx, token, "statehouse_behaviour", &b); err != nil {
		f.warn("remote config: statehouse_behaviour unavailable, using local", "error", err)
		f.recordStatus("statehouse_behaviour", err)
		return
	}
	f.recordStatus("statehouse_behaviour", nil)
	if b.Energy != nil {
		cfg.Energy = *b.Energy
	}
	if b.Availability != nil {
		cfg.Availability = *b.Availability
	}
	if b.House != nil {
		cfg.House = *b.House
	}
	if b.Adapters != nil {
		cfg.Adapters = *b.Adapters
	}
}

func (f *Fetcher) recordStatus(ns string, err error) {
	s := NamespaceStatus{OK: err == nil, FetchedAt: time.Now()}
	if err != nil {
		s.Error = err.Error()
	}
	f.mu.Lock()
	if f.statuses == nil {
		f.statuses = make(map[string]NamespaceStatus)
	}
	f.statuses[ns] = s
	f.mu.Unlock()
}

func (f *Fetcher) fetch(ctx context.Context, token, ns string, dst any) error {
	client := f.HTTPClient
	if client == nil {
		client = defaultFetchClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		f.BaseURL+"/api/v1/config/"+ns, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		io.Copy(io.Discard, resp.Body) //nolint:errcheck
		f.Tokens.Invalidate()
		return fmt.Errorf("unauthorized: token may be stale, invalidated for next retry")
	}
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, resp.Body) //nolint:errcheck
		return fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxConfigBytes+1))
	if err != nil {
		return fmt.Errorf("read body: %w", err)
	}
	if int64(len(body)) > maxConfigBytes {
		return fmt.Errorf("config response exceeds %d bytes", maxConfigBytes)
	}
	if err := json.Unmarshal(body, dst); err != nil {
		return fmt.Errorf("unmarshal: %w", err)
	}
	return nil
}

func (f *Fetcher) warn(msg string, args ...any) {
	if f.Logger != nil {
		f.Logger.Warn(msg, args...)
	}
}
