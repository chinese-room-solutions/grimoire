package app

import (
	"net/http"
	"testing"

	openai "github.com/chinese-room-solutions/llama-cpp-openai-client-go"
	"github.com/stretchr/testify/require"
)

// The wrapper is what makes the PDF path CA-aware: whatever HTTP client the
// connection settings install must be readable back, and a later swap must be
// visible to the next reader (a conversion reads it per call).
func TestGatewayClient_HTTPClientTracksSwaps(t *testing.T) {
	g := NewGatewayClient(openai.New(openai.Options{BaseURL: "http://gw"}))
	require.Same(t, http.DefaultClient, g.HTTPClient(), "unset falls back to the default")

	ca := &http.Client{}
	g.SetHTTPClient(ca)
	require.Same(t, ca, g.HTTPClient(), "the installed CA-aware client is what readers get")

	swapped := &http.Client{}
	g.SetHTTPClient(swapped)
	require.Same(t, swapped, g.HTTPClient(), "a later connection change is observed")

	g.SetHTTPClient(nil)
	require.Same(t, http.DefaultClient, g.HTTPClient(), "clearing reverts to the default")
}
