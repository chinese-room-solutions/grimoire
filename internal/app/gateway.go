package app

import (
	"net/http"
	"sync/atomic"

	openai "github.com/chinese-room-solutions/llama-cpp-openai-client-go"
)

// GatewayClient is the live MASS gateway client plus the HTTP client the
// connection settings install on it. The openai client takes an *http.Client —
// which is where a private CA's trust pool and any transport tuning live — but
// doesn't expose it, so anything that talks to the gateway over its own HTTP (the
// PDF structurizer's typed /.v1 calls) had to fall back to http.DefaultClient and
// failed on every attempt against a private-CA gateway while embedding worked.
// Recording the swap here keeps one transport for every gateway path.
//
// It satisfies mass-sdk/gui.ConnectionApplier, so the settings handlers drive the
// wrapper and a live endpoint/CA change is observed by readers that call
// HTTPClient per use.
type GatewayClient struct {
	*openai.Client
	httpClient atomic.Pointer[http.Client]
}

// NewGatewayClient wraps the client the connection settings drive. Pass the
// wrapper (not the inner client) to gui.ApplyStoredConnection and
// gui.ConnectionConfig, or the swap won't be seen.
func NewGatewayClient(c *openai.Client) *GatewayClient {
	return &GatewayClient{Client: c}
}

// SetHTTPClient applies the swap to the wrapped client and records it.
func (g *GatewayClient) SetHTTPClient(h *http.Client) {
	g.Client.SetHTTPClient(h)
	g.httpClient.Store(h)
}

// HTTPClient returns the HTTP client currently in effect: the CA-aware one once
// a connection with a stored CA has been applied, else the default the openai
// client itself falls back to. Read it per use — the settings menu can swap it
// at any time.
func (g *GatewayClient) HTTPClient() *http.Client {
	if h := g.httpClient.Load(); h != nil {
		return h
	}
	return http.DefaultClient
}
