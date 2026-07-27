package pdfconvert

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strings"
	"time"

	"github.com/KernelPryanic/ctxerr"
	"github.com/rs/zerolog"
)

// Compile-time check: Structurizer implements StructurizerInterface.
var _ StructurizerInterface = (*Structurizer)(nil)

// DefaultPageTimeout bounds one page's LLM call when the vault doesn't configure
// its own (appconfig's ConvertPageTimeout / the Vault menu). Vision models are
// slow — a page is a few thousand prompt tokens plus up to MaxTokens of
// generation — but a page that hasn't finished in ten minutes is not going to,
// and the pages behind it are queued on the same worker. Raise it per vault for
// deliberately slow hardware.
const DefaultPageTimeout = 10 * time.Minute

// systemPrompt is the structurization instructions, shared verbatim with the
// fine-tuning pipeline in pdf2doc-tune (instructions.txt).
//
//go:embed instructions.txt
var systemPrompt string

// Structurizer sends page data to a vision LLM via the gateway's typed
// /.v1/Chat endpoint. The OpenAI-compat shim doesn't carry image parts in
// the spec, so we hand-roll a tiny JSON client against the typed surface.
type Structurizer struct {
	httpClient  *http.Client
	gatewayURL  string // e.g. "http://localhost:3455/mass.llama-cpp"
	authToken   string // optional bearer token
	model       string // store-relative path to the GGUF, e.g. "publisher/repo/file.gguf"
	contextSize int32
	cacheType   string // "" / "f16" / "q8_0" / "q4_0"
	maxTokens   int32
	pageTimeout time.Duration
	logger      zerolog.Logger
}

// StructurizerConfig is what NewStructurizer takes. Kept as a struct so the
// signature doesn't grow ungainly when we add fields.
type StructurizerConfig struct {
	HTTPClient  *http.Client
	GatewayURL  string
	AuthToken   string
	Model       string
	ContextSize int32
	CacheType   string
	MaxTokens   int
	// PageTimeout bounds one page's conversion; <= 0 uses DefaultPageTimeout.
	PageTimeout time.Duration
	Logger      zerolog.Logger
}

// NewStructurizer builds a Structurizer. HTTPClient should be the gateway's own
// client (its TLS configuration is where a private CA lives); nil falls back to
// http.DefaultClient. It must NOT carry a client-wide Timeout — the result wait
// legitimately blocks for minutes — so every request here bounds itself with a
// context deadline instead: PageTimeout for the result wait, submitTimeout for
// the submit, cancelTimeout for the cancel.
func NewStructurizer(cfg StructurizerConfig) *Structurizer {
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = 4096
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = http.DefaultClient
	}
	if cfg.PageTimeout <= 0 {
		cfg.PageTimeout = DefaultPageTimeout
	}
	return &Structurizer{
		httpClient:  cfg.HTTPClient,
		gatewayURL:  strings.TrimRight(cfg.GatewayURL, "/"),
		authToken:   cfg.AuthToken,
		model:       cfg.Model,
		contextSize: cfg.ContextSize,
		cacheType:   cfg.CacheType,
		maxTokens:   int32(cfg.MaxTokens),
		pageTimeout: cfg.PageTimeout,
		logger:      cfg.Logger,
	}
}

// Structurize submits a page's job to the gateway and blocks for its structured
// HTML. Submission and the (long) durable read are split internally so the
// gateway runs the job asynchronously, but the caller sees one synchronous call.
// A per-page timeout bounds the wait.
func (s *Structurizer) Structurize(ctx context.Context, input PageInput) (string, error) {
	jobID, err := s.submitChat(ctx, input)
	if err != nil {
		return "", ctxerr.With(
			fmt.Errorf("%w: %w", ErrLLMCall, err),
			map[string]any{"page": input.PageNum},
		)
	}

	callCtx, cancel := context.WithTimeout(ctx, s.pageTimeout)
	defer cancel()

	body, err := s.awaitChat(callCtx, jobID)
	if err != nil {
		// The await is a durable read: dropping it (a cancelled import or the
		// per-page timeout) leaves the job running on the gateway, burning the
		// worker while the next page queues behind it. Tell the gateway to
		// stop the work rather than leak a running job.
		if callCtx.Err() != nil {
			s.cancelJob(jobID)
		}
		return "", ctxerr.With(
			fmt.Errorf("%w: %w", ErrLLMCall, err),
			map[string]any{"job_id": jobID, "page": input.PageNum},
		)
	}
	if body == "" {
		return "", ctxerr.With(
			fmt.Errorf("%w: empty response message", ErrLLMCall),
			map[string]any{"job_id": jobID, "page": input.PageNum},
		)
	}
	return stripControlTokens(body), nil
}

// cancelTimeout bounds the best-effort job-cancel request.
const cancelTimeout = 10 * time.Second

// cancelJob sends DELETE /.v1/Jobs/{id} so the gateway stops an abandoned job
// after a cancelled conversion. Best effort: it uses its own short-lived context
// (the conversion's is already cancelled) and only logs on failure.
func (s *Structurizer) cancelJob(jobID string) {
	if jobID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), cancelTimeout)
	defer cancel()
	if _, err := s.do(ctx, http.MethodDelete, s.gatewayURL+"/.v1/Jobs/"+jobID, nil); err != nil {
		s.logger.Warn().Err(err).Str("job_id", jobID).Msg("cancelling gateway job")
	}
}

// chatPart mirrors mass.v1.llamacpp.ContentPart in JSON. The gateway accepts
// the typed shape; OpenAI-compat does not carry image bytes natively.
type chatPart struct {
	Type     string `json:"type"` // "text" | "image" | "audio"
	Text     string `json:"text,omitempty"`
	Data     []byte `json:"data,omitempty"` // base64-encoded by encoding/json
	MIMEType string `json:"mime_type,omitempty"`
}

type chatMessage struct {
	Role    string     `json:"role"`
	Content string     `json:"content,omitempty"`
	Parts   []chatPart `json:"parts,omitempty"`
}

type loadConfig struct {
	ContextSize int32  `json:"context_size,omitempty"`
	CacheType   string `json:"cache_type,omitempty"`
}

type sampling struct {
	MaxTokens     *int32  `json:"max_tokens,omitempty"`
	Temperature   float32 `json:"temperature,omitempty"`
	MinP          float32 `json:"min_p,omitempty"`
	RepeatPenalty float32 `json:"repeat_penalty,omitempty"`
}

type chatRequest struct {
	Model    string        `json:"model"`
	Config   *loadConfig   `json:"config,omitempty"`
	Messages []chatMessage `json:"messages"`
	Sampling *sampling     `json:"sampling,omitempty"`
}

type chatResponse struct {
	Message *struct {
		Content string `json:"content"`
	} `json:"message"`
}

// submitResponse is the gateway's reply to a submit (POST /.v1/Chat): just the
// job id. The result is fetched separately via GET /.v1/Jobs/{id}?wait=1.
type submitResponse struct {
	JobID string `json:"job_id"`
}

// jobResultResponse mirrors the gateway's GET /.v1/Jobs/{id} body. Status is
// "pending" | "processing" | "done" | "error"; Result holds the chat response
// when done, Error the reason when error.
type jobResultResponse struct {
	Status string          `json:"status"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

// submitTimeout bounds the submit request. It only enqueues the job — the page
// image rides along, but the gateway answers as soon as it's stored — so a submit
// that hasn't returned by now is hung, not busy.
const submitTimeout = 2 * time.Minute

// submitChat builds and POSTs the typed /.v1/Chat request and returns the
// gateway job id. Submission only — the result is awaited separately.
func (s *Structurizer) submitChat(ctx context.Context, input PageInput) (string, error) {
	parts := make([]chatPart, 0, 2)
	if len(input.ImagePNG) > 0 {
		parts = append(parts, chatPart{Type: "image", Data: input.ImagePNG, MIMEType: "image/png"})
	}
	parts = append(parts, chatPart{Type: "text", Text: buildPageText(input)})

	req := chatRequest{
		Model: s.model,
		Messages: []chatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Parts: parts},
		},
		Sampling: &sampling{
			MaxTokens:     &s.maxTokens,
			Temperature:   0.1,
			MinP:          0.05,
			RepeatPenalty: 1.05,
		},
	}
	if s.contextSize > 0 || s.cacheType != "" {
		req.Config = &loadConfig{ContextSize: s.contextSize, CacheType: s.cacheType}
	}

	body, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("marshal chat request: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, submitTimeout)
	defer cancel()
	respBody, err := s.do(ctx, http.MethodPost, s.gatewayURL+"/.v1/Chat", body)
	if err != nil {
		return "", err
	}
	var parsed submitResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", fmt.Errorf("unmarshal submit response: %w", err)
	}
	if parsed.JobID == "" {
		return "", fmt.Errorf("%w: submit returned no job id", ErrLLMCall)
	}
	return parsed.JobID, nil
}

// awaitPollBase/awaitPollMax space successive wait-GETs when one returns before
// the job's result row is terminal. Each re-poll re-opens a Reattach stream on
// the gateway, so the delay grows instead of hammering it once a second for the
// whole page budget: the race the re-poll covers resolves in a moment, and once
// it hasn't, polling faster doesn't help. Jittered so concurrent conversions
// don't line up. Vars so tests can shrink them.
var (
	awaitPollBase = 1 * time.Second
	awaitPollMax  = 5 * time.Second
)

// awaitPollJitter spreads each poll delay by ±25%.
const awaitPollJitter = 0.25

// awaitChat fetches a job's result, blocking until terminal via ?wait=1. The
// returned content is the assistant message; a terminal error status surfaces
// as an error.
//
// A wait-GET can return with the status still "pending"/"processing": the
// gateway's wait unblocks on the job's terminal stream frame, which older
// gateways published before storing the durable result. Such a response means
// "not finished materializing", not "empty" — re-poll until terminal (the
// caller's per-page timeout bounds the loop).
func (s *Structurizer) awaitChat(ctx context.Context, jobID string) (string, error) {
	url := s.gatewayURL + "/.v1/Jobs/" + jobID + "?wait=1"
	delay := awaitPollBase
	for {
		respBody, err := s.do(ctx, http.MethodGet, url, nil)
		if err != nil {
			return "", err
		}
		var res jobResultResponse
		if err := json.Unmarshal(respBody, &res); err != nil {
			return "", fmt.Errorf("unmarshal job result: %w", err)
		}
		switch res.Status {
		case "error":
			return "", fmt.Errorf("%w: %s", ErrLLMCall, res.Error)
		case "done":
			if len(res.Result) == 0 {
				return "", nil
			}
			var chat chatResponse
			if err := json.Unmarshal(res.Result, &chat); err != nil {
				return "", fmt.Errorf("unmarshal chat result: %w", err)
			}
			if chat.Message == nil {
				return "", nil
			}
			return chat.Message.Content, nil
		default:
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(jitter(delay)):
			}
			delay = min(delay*2, awaitPollMax)
		}
	}
}

// jitter spreads d by ±awaitPollJitter.
func jitter(d time.Duration) time.Duration {
	return time.Duration(float64(d) * (1 + awaitPollJitter*(2*rand.Float64()-1)))
}

// do issues an authenticated request and returns the response body, mapping a
// non-2xx to an error with the body text.
func (s *Structurizer) do(ctx context.Context, method, url string, body []byte) ([]byte, error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rdr)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if s.authToken != "" {
		req.Header.Set("Authorization", "Bearer "+s.authToken)
	}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(respBody))
	}
	return respBody, nil
}

// buildPageText formats the text portion of a single page's user message to
// match the training-time format in pdf2doc-tune/data.py
// (build_vision_messages).
func buildPageText(input PageInput) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "=== Page %d ===", input.PageNum)

	if input.PreviousPageHTML != "" {
		sb.WriteString("\n\n--- Previous page HTML ---\n")
		sb.WriteString(input.PreviousPageHTML)
		sb.WriteString("\n--- End previous page ---")
	}

	if len(input.PreviousHeadings) > 0 {
		sb.WriteString("\n\n--- Previous headings ---\n")
		sb.WriteString(strings.Join(input.PreviousHeadings, "\n"))
		sb.WriteString("\n--- End previous headings ---")
	}

	if input.RawText != "" {
		sb.WriteString("\n\n")
		sb.WriteString(input.RawText)
	}

	return sb.String()
}

// controlTokens lists model-specific control/formatting tokens that may leak
// into inference output across different model families. Vendored from the
// retired mass-client-go package.
var controlTokens = []string{
	// GLM
	"<|begin_of_box|>", "<|end_of_box|>",
	"<|box_start|>", "<|box_end|>",
	// ChatML
	"<|im_start|>", "<|im_end|>",
	// Generic / Llama
	"<|endoftext|>", "<|eot_id|>",
	"<|start_header_id|>", "<|end_header_id|>",
	// Qwen
	"<|im_sep|>",
	// Gemma
	"<start_of_turn>", "<end_of_turn>",
}

func stripControlTokens(s string) string {
	for _, t := range controlTokens {
		s = strings.ReplaceAll(s, t, "")
	}
	return strings.TrimSpace(s)
}
