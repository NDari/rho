package registry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/NDari/rho/core/config"
	"github.com/NDari/rho/core/provider/fake"
)

func TestListModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/models":
			if r.Header.Get("anthropic-version") != "" {
				_, _ = w.Write([]byte(`{"data": [{"type": "model", "id": "claude-x", "display_name": "Claude X", "max_input_tokens": 1000000, "max_tokens": 128000,
					"capabilities": {"thinking": {"supported": true}, "image_input": {"supported": true}}}], "has_more": false}`))
				return
			}
			_, _ = w.Write([]byte(`{"object": "list", "data": [{"id": "local", "object": "model"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	ctx := context.Background()

	got, err := ListModels(ctx, "local", config.Provider{Type: config.ProviderOpenAICompat, BaseURL: srv.URL + "/v1"})
	if err != nil || len(got) != 1 || got[0].ID != "local" {
		t.Errorf("openai-compat: %+v, %v", got, err)
	}
	got, err = ListModels(ctx, "anthropic", config.Provider{Type: config.ProviderAnthropic, BaseURL: srv.URL})
	if err != nil || len(got) != 1 {
		t.Fatalf("anthropic: %+v, %v", got, err)
	}
	if m := got[0]; m.ID != "claude-x" || m.ContextWindow != 1000000 || m.MaxOutput != 128000 || !m.Capabilities || !m.Thinking || !m.Images {
		t.Errorf("anthropic model = %+v", m)
	}
	t.Setenv("RHO_TEST_MISSING_KEY", "")
	if _, err := ListModels(ctx, "x", config.Provider{BaseURL: srv.URL, APIKeyEnv: "RHO_TEST_MISSING_KEY"}); err == nil {
		t.Error("a missing key should fail")
	}
}

func TestSetConfig(t *testing.T) {
	cfg := config.Defaults()
	cfg.Providers["a"] = config.Provider{Type: config.ProviderOpenAICompat, BaseURL: "http://a"}
	cfg.Providers["b"] = config.Provider{Type: config.ProviderOpenAICompat, BaseURL: "http://b"}
	r := New(cfg)
	a1, _ := r.Get("a")
	b1, _ := r.Get("b")
	f := fake.New("f")
	r.Set("f", f)

	next := config.Defaults()
	next.Providers["a"] = cfg.Providers["a"]
	next.Providers["b"] = config.Provider{Type: config.ProviderOpenAICompat, BaseURL: "http://b2"}
	r.SetConfig(next)
	if a2, _ := r.Get("a"); a2 != a1 {
		t.Error("an unchanged provider was rebuilt")
	}
	if b2, _ := r.Get("b"); b2 == b1 {
		t.Error("a changed provider was kept")
	}
	if got, _ := r.Get("f"); got != f {
		t.Error("a provider set directly was dropped")
	}
}
