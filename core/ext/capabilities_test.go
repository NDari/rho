package ext

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/NDari/rho/core/config"
	"github.com/NDari/rho/core/ext/rpc"
	"github.com/NDari/rho/core/provider"
)

type nopAgent struct{}

func (nopAgent) Busy() bool                    { return false }
func (nopAgent) Prompt(provider.Message) error { return nil }
func (nopAgent) FollowUp(string) error         { return nil }
func (nopAgent) SetModel(string) error         { return nil }
func (nopAgent) SetActiveTools([]string) error { return nil }
func (nopAgent) CancelBy(string)               {}
func (nopAgent) Compact(context.Context) error { return nil }
func (nopAgent) Info() SessionInfo             { return SessionInfo{} }
func (nopAgent) Messages() []SessionEntry      { return nil }

// Every method Rho advertises in initialize must be served.
func TestSupportedMethodsAreServed(t *testing.T) {
	m := NewManager(Options{Config: config.Defaults(), Agent: nopAgent{}})
	h := m.handler(&Extension{})
	for _, method := range SupportedMethods {
		_, err := h(context.Background(), method, json.RawMessage(`{"text":"x","model":"m","tools":["read"]}`))
		var re *rpc.Error
		if errors.As(err, &re) && re.Code == rpc.CodeMethodNotFound {
			t.Errorf("%s is advertised but not served", method)
		}
	}
	if _, err := h(context.Background(), "no/such", nil); err == nil {
		t.Error("an unknown method should fail")
	}
}
