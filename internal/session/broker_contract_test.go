package session

import (
	"testing"

	"github.com/culiops/agent-box/internal/broker"
)

// The broker adapter is the only implementation of BrokerHooks; this is the one
// place that can import both packages, so the compiler checks the contract here.
var _ BrokerHooks = broker.Hooks{}

// brokerTokenEnv is duplicated here so session does not import broker; the
// credential_process client reads the broker's spelling, so they must match.
func TestBrokerTokenEnvMatchesTheBrokersName(t *testing.T) {
	if brokerTokenEnv != broker.TokenEnv {
		t.Fatalf("session sets %q but the broker's client reads %q", brokerTokenEnv, broker.TokenEnv)
	}
}
