package embed

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	openai "github.com/chinese-room-solutions/llama-cpp-openai-client-go"
	"github.com/stretchr/testify/require"
)

func TestDetectPrefixes(t *testing.T) {
	tests := []struct {
		model     string
		wantQuery string
		wantDoc   string
	}{
		{"Qwen3-Embedding-0.6B-Q8_0", "Instruct: Given a web search query, retrieve relevant passages that answer the query\nQuery: ", ""},
		{"nomic-embed-text-v1.5", "search_query: ", "search_document: "},
		{"multilingual-e5-large", "query: ", "passage: "},
		{"bge-m3", "Represent this sentence for searching relevant passages: ", ""},
		{"some-unknown-model", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.model, func(t *testing.T) {
			q, d := detectPrefixes(tc.model)
			require.Equal(t, tc.wantQuery, q)
			require.Equal(t, tc.wantDoc, d)
		})
	}
}

func TestWithPrefixes_OverridesOnlyNonEmpty(t *testing.T) {
	e := New(openai.New(openai.Options{}), "nomic-embed-text-v1.5").
		WithPrefixes("custom query: ", "")
	require.Equal(t, "custom query: ", e.queryPrefix)
	require.Equal(t, "search_document: ", e.docPrefix) // detected value kept.
	require.Equal(t, "search_document: ", e.DocPrefix())
}

// The gateway must receive prefixed inputs: the document prefix on Embed, the
// query prefix on EmbedQuery — while the caller's texts stay untouched.
func TestEmbed_AppliesPrefixesOnTheWire(t *testing.T) {
	var got struct {
		Input any `json:"input"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&got))
		n := 1
		if in, ok := got.Input.([]any); ok {
			n = len(in)
		}
		resp := openai.EmbedResponse{}
		for i := 0; i < n; i++ {
			resp.Data = append(resp.Data, openai.EmbedItem{Index: i, Embedding: []float64{1, 2}})
		}
		require.NoError(t, json.NewEncoder(w).Encode(resp))
	}))
	defer srv.Close()

	e := New(openai.New(openai.Options{BaseURL: srv.URL}), "nomic-embed-text-v1.5")

	_, err := e.Embed(context.Background(), []string{"alpha", "beta"})
	require.NoError(t, err)
	require.Equal(t, []any{"search_document: alpha", "search_document: beta"}, got.Input)

	_, err = e.EmbedQuery(context.Background(), "find me")
	require.NoError(t, err)
	require.Equal(t, []any{"search_query: find me"}, got.Input)
}

// A gateway that never answers (a wedged job behind a crash-looping worker)
// must surface as a deadline error instead of hanging the indexer forever.
func TestEmbedTimesOutOnHangingGateway(t *testing.T) {
	// The handler parks on an explicit channel, not r.Context(): with an
	// unread request body the server never notices the client abort, so a
	// ctx-parked handler would deadlock srv.Close.
	unblock := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		<-unblock
	}))
	defer srv.Close()
	defer close(unblock)

	e := New(openai.New(openai.Options{BaseURL: srv.URL}), "test-model")
	e.timeout = 50 * time.Millisecond

	done := make(chan error, 1)
	go func() {
		_, err := e.Embed(context.Background(), []string{"hello"})
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Embed() error = %v, want context.DeadlineExceeded", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Embed() still hanging past its deadline")
	}
}

// isModelLoading must match only the gateway's transient cold-start condition
// (a 500 whose message says the model is still loading) and nothing else, so a
// retry warms the model without masking real failures.
func TestIsModelLoading(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "model-not-loaded 500 in message",
			err:  &openai.APIError{Status: 500, Msg: "worker: AssignJob: model not loaded: qwen3-embedding"},
			want: true,
		},
		{
			name: "model-not-loaded 500 in raw body",
			err:  &openai.APIError{Status: 500, Body: `{"error":"model not loaded"}`},
			want: true,
		},
		{
			name: "case-insensitive",
			err:  &openai.APIError{Status: 500, Msg: "Model Not Loaded"},
			want: true,
		},
		{
			name: "wrapped error still matches",
			err:  fmt.Errorf("gateway embed: %w", &openai.APIError{Status: 500, Msg: "model not loaded"}),
			want: true,
		},
		{
			name: "different 500 is not retryable",
			err:  &openai.APIError{Status: 500, Msg: "out of memory"},
			want: false,
		},
		{
			name: "model-not-loaded but wrong status",
			err:  &openai.APIError{Status: 400, Msg: "model not loaded"},
			want: false,
		},
		{
			name: "plain error",
			err:  errors.New("model not loaded"),
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isModelLoading(tc.err); got != tc.want {
				t.Fatalf("isModelLoading() = %v, want %v", got, tc.want)
			}
		})
	}
}

// classify decides which failures are worth repeating. The table is the contract:
// a still-loading model waits (generously), a restarting or overloaded gateway is
// re-attempted a few times, and everything else — including a wedged request and
// a rejected certificate — fails on the first attempt.
func TestClassify(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want retryClass
	}{
		{"nil", nil, retryNone},
		{"model still loading", &openai.APIError{Status: 500, Msg: "model not loaded"}, retryLoading},
		{
			"model still loading, wrapped",
			fmt.Errorf("gateway embed: %w", &openai.APIError{Status: 500, Msg: "model not loaded"}),
			retryLoading,
		},
		{"bad gateway", &openai.APIError{Status: http.StatusBadGateway}, retryTransient},
		{"service unavailable", &openai.APIError{Status: http.StatusServiceUnavailable}, retryTransient},
		{"gateway timeout", &openai.APIError{Status: http.StatusGatewayTimeout}, retryTransient},
		{"other 500", &openai.APIError{Status: 500, Msg: "out of memory"}, retryNone},
		{"bad request", &openai.APIError{Status: http.StatusBadRequest, Msg: "no such model"}, retryNone},
		{"unauthorized", &openai.APIError{Status: http.StatusUnauthorized}, retryNone},
		{
			"connection refused",
			&url.Error{Op: "Post", URL: "http://gw", Err: &net.OpError{
				Op: "dial", Err: &os.SyscallError{Syscall: "connect", Err: syscall.ECONNREFUSED},
			}},
			retryTransient,
		},
		{
			"connection reset mid-request",
			&net.OpError{Op: "read", Err: &os.SyscallError{Syscall: "read", Err: syscall.ECONNRESET}},
			retryTransient,
		},
		{"connection dropped", fmt.Errorf("calling gw: %w", io.EOF), retryTransient},
		{"truncated response", io.ErrUnexpectedEOF, retryTransient},
		{
			"wire timeout",
			&url.Error{Op: "Post", URL: "http://gw", Err: &net.OpError{Op: "read", Err: os.ErrDeadlineExceeded}},
			retryTransient,
		},
		{
			// The per-attempt deadline: the request was wedged, not slow — repeating
			// it just burns another requestTimeout.
			"per-attempt deadline",
			&url.Error{Op: "Post", URL: "http://gw", Err: context.DeadlineExceeded},
			retryNone,
		},
		{"caller cancelled", fmt.Errorf("calling gw: %w", context.Canceled), retryNone},
		{
			// A private CA that isn't trusted is a misconfiguration, not a blip.
			"certificate rejected",
			&url.Error{Op: "Post", URL: "https://gw", Err: &tls.CertificateVerificationError{
				Err: errors.New("x509: certificate signed by unknown authority"),
			}},
			retryNone,
		},
		{"unrecognized error", errors.New("boom"), retryNone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, classify(tc.err))
		})
	}
}

// countingGateway serves status/body for every /v1/embeddings request, counting
// them, so a test can assert how many attempts a class of failure gets.
func countingGateway(t *testing.T, handler func(w http.ResponseWriter, n int)) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		handler(w, int(calls.Add(1)))
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

// newRetryEmbedder is an Embedder against url with the retry budgets shrunk to
// milliseconds, so the two classes' behavior is observable in a unit test.
func newRetryEmbedder(url string) *Embedder {
	e := New(openai.New(openai.Options{BaseURL: url}), "test-model")
	e.timeout = 2 * time.Second
	e.retry = retryPolicy{
		loadBudget:        300 * time.Millisecond,
		loadBase:          10 * time.Millisecond,
		loadMax:           40 * time.Millisecond,
		transientAttempts: transientAttempts,
		transientBase:     5 * time.Millisecond,
		transientMax:      20 * time.Millisecond,
	}
	return e
}

// A model that never loads is retried until the wall-clock budget runs out — not
// for a fixed number of attempts — and then surfaces the gateway's own error.
func TestEmbed_ModelLoadingRetriesUntilBudgetSpent(t *testing.T) {
	srv, calls := countingGateway(t, func(w http.ResponseWriter, _ int) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"model not loaded"}}`))
	})

	e := newRetryEmbedder(srv.URL)
	start := time.Now()
	_, err := e.Embed(context.Background(), []string{"hello"})
	elapsed := time.Since(start)

	require.ErrorContains(t, err, "model not loaded")
	require.Greater(t, calls.Load(), int64(3), "a load wait spans many attempts, not a fixed few")
	require.Less(t, elapsed, 5*time.Second, "the budget must bound the wait")
}

// A restarting gateway (503) gets exactly transientAttempts tries — enough to
// ride out a restart, few enough not to sit on a broken gateway.
func TestEmbed_TransientErrorGetsAFewAttempts(t *testing.T) {
	srv, calls := countingGateway(t, func(w http.ResponseWriter, _ int) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})

	e := newRetryEmbedder(srv.URL)
	_, err := e.Embed(context.Background(), []string{"hello"})

	require.Error(t, err)
	require.EqualValues(t, transientAttempts, calls.Load())
}

// The retry is what makes a gateway restart survivable: the blip clears and the
// same call returns vectors.
func TestEmbed_RecoversFromTransientError(t *testing.T) {
	srv, calls := countingGateway(t, func(w http.ResponseWriter, n int) {
		if n == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(openai.EmbedResponse{
			Data: []openai.EmbedItem{{Index: 0, Embedding: []float64{1, 2}}},
		})
	})

	e := newRetryEmbedder(srv.URL)
	vecs, err := e.Embed(context.Background(), []string{"hello"})

	require.NoError(t, err)
	require.Len(t, vecs, 1)
	require.EqualValues(t, 2, calls.Load())
}

// Anything outside the two transient classes fails on the first attempt.
func TestEmbed_FailFastErrorIsNotRetried(t *testing.T) {
	srv, calls := countingGateway(t, func(w http.ResponseWriter, _ int) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"no such model"}}`))
	})

	e := newRetryEmbedder(srv.URL)
	_, err := e.Embed(context.Background(), []string{"hello"})

	require.ErrorContains(t, err, "no such model")
	require.EqualValues(t, 1, calls.Load())
}

// A cancelled context short-circuits the backoff sleep instead of serving out
// the load budget.
func TestEmbed_CancelStopsRetrying(t *testing.T) {
	srv, _ := countingGateway(t, func(w http.ResponseWriter, _ int) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"model not loaded"}}`))
	})

	e := newRetryEmbedder(srv.URL)
	e.retry.loadBudget = time.Minute // long enough that only the cancel can end it.
	e.retry.loadBase = 50 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()

	start := time.Now()
	_, err := e.Embed(ctx, []string{"hello"})

	require.ErrorIs(t, err, context.Canceled)
	require.Less(t, time.Since(start), 5*time.Second, "cancel must not wait out the budget")
}
