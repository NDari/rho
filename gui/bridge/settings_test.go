package bridge

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NDari/rho/core/app"
	"github.com/NDari/rho/core/config"
)

func TestConfigFormAndSave(t *testing.T) {
	b, _ := newTestBridge(t)
	f, err := b.ConfigForm()
	if err != nil {
		t.Fatal(err)
	}
	if !f.Running || f.Global.Exists || len(f.Fields) == 0 || len(f.ProviderFields) == 0 || len(f.ModelFields) == 0 {
		t.Fatalf("form = %+v", f)
	}
	t.Setenv("RHO_TEST_KEY", "x")
	if _, err := b.SaveConfig(false, f.Global.Version, []config.Edit{
		{Kind: config.EditProvider, Name: "local", Fields: map[string]any{"base_url": "http://localhost:9931/v1", "api_key_env": "RHO_TEST_KEY"}},
		{Kind: config.EditModel, Provider: "local", Index: -1, Fields: map[string]any{"id": "m"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.core().Agent.Config().ResolveModel("m"); err != nil {
		t.Errorf("the open project didn't pick up the new model: %v", err)
	}
	f, _ = b.ConfigForm()
	if k := f.Keys["local"]; k.Env != "RHO_TEST_KEY" || !k.Set {
		t.Errorf("key status = %+v", k)
	}
	if _, err := b.SaveConfig(false, "stale", []config.Edit{{Kind: config.EditSet, Key: "max_retries", Value: 1.0}}); !errors.Is(err, config.ErrFileChanged) {
		t.Errorf("stale version: %v", err)
	}
}

func TestConfigFormOnTheStartupScreen(t *testing.T) {
	home := t.TempDir()
	t.Setenv("RHO_HOME", home)
	if err := os.WriteFile(filepath.Join(home, config.FileName), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	b := New(newFakeHost(), func(dir string, _ bool) (*app.App, error) { return app.New(app.Options{Cwd: dir, NoExtensions: true}) }, t.TempDir())
	t.Cleanup(b.close)
	if b.Startup().Error == "" {
		t.Fatal("start-up should fail without a model")
	}
	f, err := b.ConfigForm()
	if err != nil {
		t.Fatal(err)
	}
	if f.Running || !strings.Contains(f.Error, "no model configured") {
		t.Fatalf("running %v, error %q", f.Running, f.Error)
	}
	if _, err := b.SaveConfig(false, f.Global.Version, []config.Edit{
		{Kind: config.EditModel, Provider: "openai", Index: -1, Fields: map[string]any{"id": "m", "name": "gpt"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := b.Retry(); err != nil {
		t.Errorf("start-up still fails after the fix: %v", err)
	}
}
