// Package embed adapts the MASS gateway's embeddings endpoint to the indexer's
// EmbedderInterface.
package embed

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"strings"
	"time"

	openai "github.com/chinese-room-solutions/llama-cpp-openai-client-go"
)

// maxBatch caps how many texts go in one /v1/embeddings request, so a large
// note's chunk set is sent in bounded batches.
const maxBatch = 64

// A failed embed request falls into one of two transient classes, each with its
// own budget, or is a real error surfaced at once (see classify).
//
// Model still loading: the gateway answers "model not loaded" while a worker
// warms the GGUF on first use (or after eviction). A cold load genuinely takes
// minutes, so the wait is bounded by total wall clock rather than by an attempt
// count — a slow load isn't cut short by a handful of quick failures, and one
// that never finishes still gives up. The budget bounds the waiting; an attempt
// already in flight when it runs out finishes under its own requestTimeout.
//
// Transient transport/server error: a restarting or overloaded gateway refuses
// the connection, drops it, or answers 502/503/504. The first attempt is exactly
// when a retry helps, but the condition clears in seconds or not at all, so the
// budget is a few quick attempts.
//
// Both back off exponentially with jitter: without it every indexing goroutine
// that hit the same cold model retries in lockstep, and with IndexConcurrency
// workers embedding against one warming model that is a synchronised herd.
const (
	loadRetryBudget = 10 * time.Minute
	loadBackoffBase = 500 * time.Millisecond
	loadBackoffMax  = 5 * time.Second

	transientAttempts    = 3 // the first attempt plus two retries.
	transientBackoffBase = 1 * time.Second
	transientBackoffMax  = 4 * time.Second

	// backoffJitter spreads each sleep by ±25%, so concurrent workers desynchronize.
	backoffJitter = 0.25
)

// requestTimeout bounds one /v1/embeddings request. The gateway holds the
// HTTP request open while its job sits in the worker queue, so a wedged job
// (e.g. a crash-looping worker) otherwise hangs the indexer forever with no
// progress and no error. A cold model load plus a full maxBatch request on
// slow hardware fits comfortably; a request that can't finish in this window
// is wedged, not slow.
const requestTimeout = 5 * time.Minute

// LimiterInterface bounds how many Embed calls run at once. acquire blocks for a
// slot (returning a token for release, or ctx.Err()); release frees that token.
// A nil limiter means unbounded. The app supplies a shared, resizable limiter so
// one concurrency setting caps embedding across every path (reindex, import,
// external-change watcher) that goes through the same Embedder.
type LimiterInterface interface {
	Acquire(ctx context.Context) (token any, err error)
	Release(token any)
}

// family maps a recognizable model-id substring to the instruction prefixes its
// family expects. Modern embedding models are asymmetric: queries are embedded
// with an instruction, documents plain (or with their own marker); skipping the
// prefixes measurably degrades retrieval. First substring match wins.
type family struct {
	match       string
	queryPrefix string
	docPrefix   string
}

var families = []family{
	{
		match:       "qwen3-embedding",
		queryPrefix: "Instruct: Given a web search query, retrieve relevant passages that answer the query\nQuery: ",
	},
	{
		match:       "nomic",
		queryPrefix: "search_query: ",
		docPrefix:   "search_document: ",
	},
	{
		match:       "e5",
		queryPrefix: "query: ",
		docPrefix:   "passage: ",
	},
	{
		match:       "bge",
		queryPrefix: "Represent this sentence for searching relevant passages: ",
	},
}

// detectPrefixes returns the query/document prefixes for a model id, or empty
// strings for an unrecognized family.
func detectPrefixes(model string) (query, doc string) {
	id := strings.ToLower(model)
	for _, f := range families {
		if strings.Contains(id, f.match) {
			return f.queryPrefix, f.docPrefix
		}
	}
	return "", ""
}

// retryPolicy holds the two classes' budgets, so tests can shrink them.
type retryPolicy struct {
	loadBudget        time.Duration
	loadBase, loadMax time.Duration
	transientAttempts int
	transientBase     time.Duration
	transientMax      time.Duration
}

func defaultRetryPolicy() retryPolicy {
	return retryPolicy{
		loadBudget:        loadRetryBudget,
		loadBase:          loadBackoffBase,
		loadMax:           loadBackoffMax,
		transientAttempts: transientAttempts,
		transientBase:     transientBackoffBase,
		transientMax:      transientBackoffMax,
	}
}

// Embedder calls a MASS gateway to embed text with a chosen model.
type Embedder struct {
	client      *openai.Client
	model       string
	limiter     LimiterInterface // nil = unbounded.
	timeout     time.Duration    // per-request deadline; tests shrink it.
	retry       retryPolicy      // retry budgets; tests shrink them.
	queryPrefix string
	docPrefix   string
}

// New builds an Embedder for the given gateway client and embedding model id,
// auto-detecting the model family's query/document prefixes.
func New(client *openai.Client, model string) *Embedder {
	q, d := detectPrefixes(model)
	return &Embedder{
		client:      client,
		model:       model,
		timeout:     requestTimeout,
		retry:       defaultRetryPolicy(),
		queryPrefix: q,
		docPrefix:   d,
	}
}

// WithLimiter returns the embedder using lim to bound concurrent Embed calls, so
// every embedding path shares one global limit.
func (e *Embedder) WithLimiter(lim LimiterInterface) *Embedder {
	e.limiter = lim
	return e
}

// WithPrefixes overrides the auto-detected prefixes; an empty value keeps the
// detected one.
func (e *Embedder) WithPrefixes(query, doc string) *Embedder {
	if query != "" {
		e.queryPrefix = query
	}
	if doc != "" {
		e.docPrefix = doc
	}
	return e
}

// DocPrefix returns the document prefix in effect. The index store fingerprints
// on it: a different prefix produces different vectors, so the index rebuilds.
func (e *Embedder) DocPrefix() string {
	return e.docPrefix
}

// Embed returns one document vector per input text, in order, batching requests
// and applying the family's document prefix. The whole call counts as one unit
// against the limiter (a note's chunks embed together), so the limit bounds
// concurrent notes-in-flight.
func (e *Embedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	return e.embed(ctx, texts, e.docPrefix)
}

// EmbedQuery embeds one search query with the family's query prefix.
func (e *Embedder) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	vecs, err := e.embed(ctx, []string{text}, e.queryPrefix)
	if err != nil {
		return nil, err
	}
	return vecs[0], nil
}

func (e *Embedder) embed(ctx context.Context, texts []string, prefix string) ([][]float32, error) {
	if e.model == "" {
		return nil, fmt.Errorf("no embedding model configured")
	}
	if e.limiter != nil {
		token, err := e.limiter.Acquire(ctx)
		if err != nil {
			return nil, err
		}
		defer e.limiter.Release(token)
	}
	if prefix != "" {
		prefixed := make([]string, len(texts))
		for i, t := range texts {
			prefixed[i] = prefix + t
		}
		texts = prefixed
	}
	out := make([][]float32, 0, len(texts))
	for start := 0; start < len(texts); start += maxBatch {
		end := min(start+maxBatch, len(texts))
		vecs, err := e.embedBatch(ctx, texts[start:end])
		if err != nil {
			return nil, err
		}
		out = append(out, vecs...)
	}
	return out, nil
}

func (e *Embedder) embedBatch(ctx context.Context, batch []string) ([][]float32, error) {
	resp, err := e.embedWithRetry(ctx, batch)
	if err != nil {
		return nil, fmt.Errorf("gateway embed: %w", err)
	}
	if len(resp.Data) != len(batch) {
		return nil, fmt.Errorf("embed returned %d vectors for %d inputs", len(resp.Data), len(batch))
	}
	// The gateway may return items out of order; place each by its Index.
	out := make([][]float32, len(batch))
	for _, item := range resp.Data {
		if item.Index < 0 || item.Index >= len(batch) {
			return nil, fmt.Errorf("embed item index %d out of range", item.Index)
		}
		out[item.Index] = toFloat32(item.Embedding)
	}
	for i, v := range out {
		if v == nil {
			return nil, fmt.Errorf("embed missing vector for input %d", i)
		}
	}
	return out, nil
}

// embedWithRetry issues the embeddings request, retrying a still-loading model
// within its wall-clock budget and a transient gateway failure for a few quick
// attempts. Anything else — including a cancelled context — returns at once; an
// exhausted budget surfaces the last error from the gateway.
func (e *Embedder) embedWithRetry(ctx context.Context, batch []string) (*openai.EmbedResponse, error) {
	req := openai.EmbedRequest{Model: e.model}.Multi(batch)
	start := time.Now()
	loadWait, transientWait := e.retry.loadBase, e.retry.transientBase
	transientLeft := e.retry.transientAttempts - 1
	for {
		resp, err := e.embedOnce(ctx, req)
		if err == nil {
			return resp, nil
		}
		var wait time.Duration
		switch classify(err) {
		case retryLoading:
			// Bounded by wall clock: stop once the next sleep would spend more than
			// the budget allows, rather than after a fixed number of attempts.
			if time.Since(start)+loadWait >= e.retry.loadBudget {
				return nil, err
			}
			wait, loadWait = loadWait, grow(loadWait, e.retry.loadMax)
		case retryTransient:
			if transientLeft <= 0 {
				return nil, err
			}
			transientLeft--
			wait, transientWait = transientWait, grow(transientWait, e.retry.transientMax)
		case retryNone:
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(jitter(wait)):
		}
	}
}

// grow doubles a backoff, capped at limit.
func grow(d, limit time.Duration) time.Duration {
	return min(d*2, limit)
}

// jitter spreads d by ±backoffJitter, so workers that failed together don't
// retry together.
func jitter(d time.Duration) time.Duration {
	spread := 1 + backoffJitter*(2*rand.Float64()-1)
	return time.Duration(float64(d) * spread)
}

// retryClass is how embedWithRetry treats a failed attempt.
type retryClass int

const (
	// retryNone fails fast: the error won't clear by being repeated.
	retryNone retryClass = iota
	// retryLoading waits out a model load, bounded by loadRetryBudget.
	retryLoading
	// retryTransient re-attempts a gateway blip a few times.
	retryTransient
)

// classify sorts a failed attempt into its retry class. It is deliberately
// narrow: only a still-loading model and a gateway that is restarting,
// overloaded, or dropped the connection are retried. A gateway that answered
// with any other status has given its verdict, and a context deadline means the
// request was wedged, not slow — repeating either just burns another timeout.
func classify(err error) retryClass {
	if err == nil {
		return retryNone
	}
	if isModelLoading(err) {
		return retryLoading
	}
	// Our own per-attempt deadline (or the caller's) expiring, and cancellation:
	// not the gateway's doing, and not fixed by trying again.
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return retryNone
	}
	var apiErr *openai.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.Status {
		case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			return retryTransient // restarting, or shedding load.
		default:
			return retryNone
		}
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return retryTransient // a dial/read timeout on the wire.
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return retryTransient // the gateway closed the connection mid-flight.
	}
	// A dial that never connected (connection refused: nothing listening yet), a
	// connection reset mid-request, or a failed TLS handshake against a gateway
	// that is coming back up.
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return retryTransient
	}
	return retryNone
}

// embedOnce issues one embeddings request under the per-request deadline.
func (e *Embedder) embedOnce(ctx context.Context, req openai.EmbedRequest) (*openai.EmbedResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()
	return e.client.Embed(ctx, req)
}

// isModelLoading reports whether err is the gateway's transient "model not
// loaded" condition — a 500 raised while a worker warms the model on first use.
func isModelLoading(err error) bool {
	var apiErr *openai.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 500 {
		return false
	}
	return strings.Contains(strings.ToLower(apiErr.Msg+apiErr.Body), "model not loaded")
}

// Dimension returns the embedding dimension of the configured model by embedding
// a probe string. The index store is built around this value.
func (e *Embedder) Dimension(ctx context.Context) (int, error) {
	vecs, err := e.Embed(ctx, []string{"dimension probe"})
	if err != nil {
		return 0, err
	}
	if len(vecs) != 1 || len(vecs[0]) == 0 {
		return 0, fmt.Errorf("probe returned no embedding")
	}
	return len(vecs[0]), nil
}

func toFloat32(in []float64) []float32 {
	out := make([]float32, len(in))
	for i, v := range in {
		out[i] = float32(v)
	}
	return out
}
