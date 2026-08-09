package pdfconvert

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// gatewayServer mimics the gateway's submit/fetch split: POST /.v1/Chat returns
// {job_id} and captures the request body into *out; GET /.v1/Jobs/{id}?wait=1
// returns the structured content as a done result.
func gatewayServer(t *testing.T, respContent string, out *map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/.v1/Chat":
			body, _ := io.ReadAll(r.Body)
			if out != nil {
				_ = json.Unmarshal(body, out)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"job_id": "job-1"})
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/.v1/Jobs/"):
			require.Equal(t, "1", r.URL.Query().Get("wait"), "result fetch must block")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "done",
				"result": map[string]any{
					"id":      "cpp-1",
					"message": map[string]any{"role": "assistant", "content": respContent},
				},
			})
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func newTestStructurizer(url string) *Structurizer {
	return NewStructurizer(StructurizerConfig{
		GatewayURL:  url,
		Model:       "test-model",
		ContextSize: 8192,
		CacheType:   "q8_0",
		MaxTokens:   4096,
	})
}

// shrinkPollBackoff runs the non-terminal re-poll backoff in milliseconds, so a
// test that exercises several polls stays fast.
func shrinkPollBackoff(t *testing.T) {
	t.Helper()
	prevBase, prevMax := awaitPollBase, awaitPollMax
	awaitPollBase, awaitPollMax = 5*time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() { awaitPollBase, awaitPollMax = prevBase, prevMax })
}

func TestStructurizer_WithImage(t *testing.T) {
	var got map[string]any
	server := gatewayServer(t, "<h1>Structured Output</h1><p>Some text.</p>", &got)
	defer server.Close()

	s := newTestStructurizer(server.URL)
	result, err := s.Structurize(context.Background(), PageInput{
		PageNum:  1,
		RawText:  "Hello World",
		ImagePNG: []byte{0x89, 'P', 'N', 'G'},
	})
	require.NoError(t, err)
	require.Contains(t, result, "Structured Output")

	require.Equal(t, "test-model", got["model"])

	messages := got["messages"].([]any)
	require.Len(t, messages, 2, "system + user message")

	require.Equal(t, "system", messages[0].(map[string]any)["role"])
	userMsg := messages[1].(map[string]any)
	require.Equal(t, "user", userMsg["role"])
	parts := userMsg["parts"].([]any)
	require.Len(t, parts, 2, "image + text")

	imgPart := parts[0].(map[string]any)
	require.Equal(t, "image", imgPart["type"])
	require.NotEmpty(t, imgPart["data"])
	require.Equal(t, "image/png", imgPart["mime_type"])

	textPart := parts[1].(map[string]any)
	require.Equal(t, "text", textPart["type"])
	require.Contains(t, textPart["text"], "=== Page 1 ===")
	require.Contains(t, textPart["text"], "Hello World")

	sampling := got["sampling"].(map[string]any)
	require.Equal(t, float64(4096), sampling["max_tokens"])
	require.InDelta(t, 0.1, sampling["temperature"], 1e-6)

	cfg := got["config"].(map[string]any)
	require.Equal(t, "q8_0", cfg["cache_type"])
	require.Equal(t, float64(8192), cfg["context_size"])
}

func TestStructurizer_WithContext(t *testing.T) {
	var got map[string]any
	server := gatewayServer(t, "<h2>Section 2</h2><p>Continued.</p>", &got)
	defer server.Close()

	s := newTestStructurizer(server.URL)
	result, err := s.Structurize(context.Background(), PageInput{
		PageNum:          2,
		RawText:          "Continued text here",
		ImagePNG:         []byte{0x89, 'P', 'N', 'G'},
		PreviousPageHTML: "<h1>Chapter 1</h1><p>Intro.</p>",
		PreviousHeadings: []string{"<h1>Chapter 1</h1>"},
	})
	require.NoError(t, err)
	require.Contains(t, result, "Section 2")

	messages := got["messages"].([]any)
	parts := messages[1].(map[string]any)["parts"].([]any)
	require.Len(t, parts, 2, "image + text")

	txt := parts[1].(map[string]any)["text"].(string)
	require.Contains(t, txt, "=== Page 2 ===")
	require.Contains(t, txt, "Previous page HTML")
	require.Contains(t, txt, "<h1>Chapter 1</h1><p>Intro.</p>")
	require.Contains(t, txt, "Previous headings")
	require.Contains(t, txt, "<h1>Chapter 1</h1>")
	require.Contains(t, txt, "Continued text here")
}

func TestStructurizer_TextOnly(t *testing.T) {
	var got map[string]any
	server := gatewayServer(t, "<p>Text-only output.</p>", &got)
	defer server.Close()

	s := newTestStructurizer(server.URL)
	result, err := s.Structurize(context.Background(), PageInput{PageNum: 1, RawText: "Hello World"})
	require.NoError(t, err)
	require.Contains(t, result, "Text-only")

	messages := got["messages"].([]any)
	parts := messages[1].(map[string]any)["parts"].([]any)
	require.Len(t, parts, 1, "text only")
	require.Equal(t, "text", parts[0].(map[string]any)["type"])
}

func TestStructurizer_ErrorStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			_ = json.NewEncoder(w).Encode(map[string]any{"job_id": "bad"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "error", "error": "model not loaded"})
	}))
	defer server.Close()

	s := newTestStructurizer(server.URL)
	_, err := s.Structurize(context.Background(), PageInput{PageNum: 1, RawText: "Hello"})
	require.Error(t, err)
	require.ErrorIs(t, err, ErrLLMCall)
}

// Whatever ends the await, Structurize must not leave the gateway grinding a
// result nobody will read — the next page queues behind it on the same worker.
// So every failed await cancels the job (DELETE /.v1/Jobs/{id}), except one the
// gateway already settled, which has nothing left to stop.
func TestStructurizer_CancelsAbandonedJob(t *testing.T) {
	blockUntilDropped := func(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() }

	tests := []struct {
		name        string
		jobID       string
		awaitGET    http.HandlerFunc
		cancelAfter time.Duration // > 0: cancel the caller's context after this
		pageTimeout time.Duration // > 0: override the per-page timeout
		wantCancel  bool
	}{
		{
			name:        "caller cancels mid-await",
			jobID:       "job-9",
			awaitGET:    blockUntilDropped,
			cancelAfter: 50 * time.Millisecond,
			wantCancel:  true,
		},
		{
			name:        "per-page timeout mid-await",
			jobID:       "job-42",
			awaitGET:    blockUntilDropped,
			pageTimeout: 50 * time.Millisecond,
			wantCancel:  true,
		},
		{
			name:  "gateway fails the await request",
			jobID: "job-502",
			awaitGET: func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "bad gateway", http.StatusBadGateway)
			},
			wantCancel: true,
		},
		{
			name:  "await answers with an unparseable body",
			jobID: "job-junk",
			awaitGET: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte("<html>not json</html>"))
			},
			wantCancel: true,
		},
		{
			name:  "gateway already failed the job",
			jobID: "job-settled",
			awaitGET: func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{"status": "error", "error": "model not loaded"})
			},
			wantCancel: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deleted := make(chan string, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.Method {
				case http.MethodPost: // submit returns a job id
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]any{"job_id": tt.jobID})
				case http.MethodGet:
					tt.awaitGET(w, r)
				case http.MethodDelete:
					deleted <- r.URL.Path
					w.WriteHeader(http.StatusNoContent)
				}
			}))
			defer server.Close()

			s := newTestStructurizer(server.URL)
			if tt.pageTimeout > 0 {
				s.pageTimeout = tt.pageTimeout
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tt.cancelAfter > 0 {
				go func() { time.Sleep(tt.cancelAfter); cancel() }()
			}

			_, err := s.Structurize(ctx, PageInput{PageNum: 1, RawText: "x"})
			require.Error(t, err)

			if !tt.wantCancel {
				// cancelJob runs before Structurize returns, so an empty channel
				// here means it was never called.
				require.Empty(t, deleted, "a settled job must not be cancelled")
				return
			}
			select {
			case path := <-deleted:
				require.Equal(t, "/.v1/Jobs/"+tt.jobID, path)
			case <-time.After(2 * time.Second):
				t.Fatal("expected a DELETE to cancel the job")
			}
		})
	}
}

// A wait-GET can return while the job is still "processing" — the gateway's
// wait unblocks on the terminal stream frame, which can beat the durable
// result store. Structurize must re-poll until terminal instead of treating
// the done-less body as an empty response (that path silently lost finished
// pages as "empty response message").
func TestStructurizer_RepollsUntilTerminal(t *testing.T) {
	shrinkPollBackoff(t)

	var gets int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodPost:
			_ = json.NewEncoder(w).Encode(map[string]any{"job_id": "job-7"})
		case http.MethodGet:
			gets++
			if gets < 3 {
				// The racy shape: wait returned, result row not terminal yet.
				_ = json.NewEncoder(w).Encode(map[string]any{"status": "processing"})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "done",
				"result": map[string]any{
					"id":      "cpp-7",
					"message": map[string]any{"role": "assistant", "content": "<p>Late but whole.</p>"},
				},
			})
		}
	}))
	defer server.Close()

	s := newTestStructurizer(server.URL)
	result, err := s.Structurize(context.Background(), PageInput{PageNum: 1, RawText: "Hello"})
	require.NoError(t, err)
	require.Contains(t, result, "Late but whole")
	require.Equal(t, 3, gets, "must re-poll past non-terminal wait returns")
}

func TestStructurizer_SubmitAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"model not loaded"}`))
	}))
	defer server.Close()

	s := newTestStructurizer(server.URL)
	_, err := s.Structurize(context.Background(), PageInput{PageNum: 1, RawText: "Hello"})
	require.Error(t, err)
	require.ErrorIs(t, err, ErrLLMCall)
}

// The per-page budget is a default, not a hard-coded constant: an unset config
// gets DefaultPageTimeout, a configured one is honored verbatim.
func TestNewStructurizer_PageTimeout(t *testing.T) {
	cases := []struct {
		name string
		set  time.Duration
		want time.Duration
	}{
		{"unset falls back to the default", 0, DefaultPageTimeout},
		{"negative falls back to the default", -time.Second, DefaultPageTimeout},
		{"configured value is used", 25 * time.Minute, 25 * time.Minute},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := NewStructurizer(StructurizerConfig{GatewayURL: "http://gw", PageTimeout: tc.set})
			require.Equal(t, tc.want, s.pageTimeout)
		})
	}
}

// The re-poll of a non-terminal wait-GET must back off, not hammer the gateway
// once per fixed interval for the whole page budget — every poll re-opens a
// Reattach stream there. Successive gaps grow (jitter can shave a single gap, so
// the assertion is on the span across several polls) and stay under the cap.
func TestStructurizer_PollBackoffGrows(t *testing.T) {
	shrinkPollBackoff(t)

	var mu sync.Mutex
	var gets []time.Time
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			_ = json.NewEncoder(w).Encode(map[string]any{"job_id": "job-b"})
			return
		}
		mu.Lock()
		gets = append(gets, time.Now())
		n := len(gets)
		mu.Unlock()
		if n < 5 {
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "processing"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "done",
			"result": map[string]any{"message": map[string]any{"content": "<p>done</p>"}},
		})
	}))
	defer server.Close()

	s := newTestStructurizer(server.URL)
	_, err := s.Structurize(context.Background(), PageInput{PageNum: 1, RawText: "x"})
	require.NoError(t, err)

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, gets, 5)
	first := gets[1].Sub(gets[0])
	last := gets[4].Sub(gets[3])
	// Exponential 5→10→20ms puts the last gap at ≥2.4× the first even at the
	// jitter extremes; a flat delay could never clear 2×.
	require.Greater(t, last, 2*first, "the poll delay must grow between attempts")
	// Base 5ms doubling to a 20ms cap, plus ±25% jitter: no gap may exceed the cap.
	require.Less(t, last, 2*awaitPollMax, "the poll delay must stay capped")
}

// countingTransport counts the requests that ride a given http.Client.
type countingTransport struct {
	inner http.RoundTripper
	calls atomic.Int64
}

func (t *countingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	t.calls.Add(1)
	return t.inner.RoundTrip(r)
}

// Every request must ride the configured HTTP client: that client's transport is
// where the gateway's private CA is trusted. Handing the structurizer
// http.DefaultClient instead (as the app once did) failed every conversion
// against a private-CA gateway while embedding worked.
func TestStructurizer_UsesConfiguredHTTPClient(t *testing.T) {
	server := gatewayServer(t, "<p>ok</p>", nil)
	defer server.Close()

	tr := &countingTransport{inner: http.DefaultTransport}
	s := NewStructurizer(StructurizerConfig{
		GatewayURL: server.URL,
		Model:      "test-model",
		HTTPClient: &http.Client{Transport: tr},
	})
	_, err := s.Structurize(context.Background(), PageInput{PageNum: 1, RawText: "x"})
	require.NoError(t, err)
	require.EqualValues(t, 2, tr.calls.Load(), "the submit and the result wait both ride it")
}
