// Package registry constructs providers from configuration.
package registry

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/NDari/rho/core/config"
	"github.com/NDari/rho/core/provider"
	"github.com/NDari/rho/core/provider/openaicompat"
	"github.com/NDari/rho/core/provider/ratelimit"
)

// expandHome expands a leading "~/" in a path.
func expandHome(path string) string {
	if rest, ok := strings.CutPrefix(path, "~/"); ok {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, rest)
		}
	}
	return path
}

// Registry caches provider instances by name.
type Registry struct {
	mu        sync.Mutex
	cfg       config.Config
	providers map[string]provider.Provider
}

// New creates a registry.
func New(cfg config.Config) *Registry {
	return &Registry{cfg: cfg, providers: map[string]provider.Provider{}}
}

// Get returns the named provider.
func (r *Registry) Get(name string) (provider.Provider, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if p, ok := r.providers[name]; ok {
		return p, nil
	}
	pc, ok := r.cfg.Providers[name]
	if !ok {
		return nil, fmt.Errorf("unknown provider %q", name)
	}
	key := pc.APIKey
	if key == "" && pc.APIKeyEnv != "" {
		key = os.Getenv(pc.APIKeyEnv)
		if key == "" {
			return nil, fmt.Errorf("provider %q: environment variable %s is not set", name, pc.APIKeyEnv)
		}
	}
	opts := openaicompat.Options{
		Name:     name,
		BaseURL:  pc.BaseURL,
		APIKey:   key,
		Headers:  pc.Headers,
		Flavor:   pc.Flavor,
		DebugLog: expandHome(pc.DebugLog),
	}
	var p provider.Provider
	switch pc.Type {
	case config.ProviderOpenAICompat, "":
		p = openaicompat.New(opts)
	case config.ProviderOpenAIResponses:
		p = openaicompat.NewResponses(opts)
	default:
		return nil, fmt.Errorf("provider %q: unsupported type %q", name, pc.Type)
	}
	if pc.RequestsPerMinute > 0 || pc.TokensPerMinute > 0 {
		p = ratelimit.Wrap(p, ratelimit.New(ratelimit.Limits{Requests: pc.RequestsPerMinute, Tokens: pc.TokensPerMinute}))
	}
	r.providers[name] = p
	return p, nil
}

// Set registers a provider instance directly (used by tests).
func (r *Registry) Set(name string, p provider.Provider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providers[name] = p
}
