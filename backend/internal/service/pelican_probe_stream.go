package service

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/tidwall/gjson"
)

// Probe stream / retry limits.
const (
	// pelicanProbeMaxContentBytes caps the accumulated model output (4MiB). It is
	// the same budget the previous non-streaming implementation applied to the
	// whole response body, so a runaway upstream can never grow the probe's
	// memory without bound.
	pelicanProbeMaxContentBytes = 4 << 20
	// pelicanProbeMaxErrorSnippetBytes caps the upstream body embedded in an
	// error message (surfaced to admins, so it stays short).
	pelicanProbeMaxErrorSnippetBytes = 500
	// pelicanProbeMaxStreamLineBytes caps one stream line. It equals the total
	// content cap: a single oversized chunk can never need more than that, and
	// the scanner buffer stays bounded.
	pelicanProbeMaxStreamLineBytes = pelicanProbeMaxContentBytes
	// pelicanProbeStreamBufferBytes is the initial scanner buffer. SSE chunks are
	// small, but a gateway may coalesce several tokens into one line.
	pelicanProbeStreamBufferBytes = 64 << 10
)

// pelicanProbeRetryBackoffs is the backoff ladder used for transient upstream
// failures: one entry per retry attempt (so 3 retries after the first try).
//
// A probe is a background job and can afford to wait. Production showed probes
// racing the user's own traffic for the same per-user concurrency quota and
// failing with `gateway_concurrency_limit`; a short wait is usually enough.
// The whole ladder (17s) is part of the 10 minute run budget.
var pelicanProbeRetryBackoffs = []time.Duration{2 * time.Second, 5 * time.Second, 10 * time.Second}

// probeHTTPError carries the upstream status code so retry decisions do not have
// to re-parse the (already truncated) message.
type probeHTTPError struct {
	status  int
	message string
}

func (e *probeHTTPError) Error() string { return e.message }

// retryBackoffs returns the retry ladder this service instance uses. A nil field
// means the production default; tests inject a short ladder so the retry tests
// do not sleep for seconds.
func (s *PelicanProbeService) retryBackoffs() []time.Duration {
	if len(s.retryBackoffLadder) > 0 {
		return s.retryBackoffLadder
	}
	return pelicanProbeRetryBackoffs
}

// doProbeChatCompletion sends one probe request and retries transient upstream
// failures with exponential backoff.
//
// On a 2xx response the body is left open for the caller to stream. Non-2xx
// responses are drained and closed internally and turned into a
// `upstream HTTP <status>: <snippet>` error (the message shape the admin UI
// already displays); that error is retried only when the status/body marks a
// transient condition.
func (s *PelicanProbeService) doProbeChatCompletion(ctx context.Context, url, apiKey string, body []byte) (*http.Response, error) {
	backoffs := s.retryBackoffs()

	for attempt := 0; ; attempt++ {
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Authorization", "Bearer "+strings.TrimSpace(apiKey))

		resp, doErr := s.httpClient.Do(httpReq)
		switch {
		case doErr == nil && resp.StatusCode >= 200 && resp.StatusCode < 300:
			return resp, nil
		case doErr == nil:
			doErr = probeUpstreamStatusError(resp)
		}

		if attempt >= len(backoffs) || !retryableProbeFailure(ctx, doErr) {
			return nil, doErr
		}
		delay := backoffs[attempt]
		logger.LegacyPrintf(
			"service.pelican_probe",
			"[PelicanProbe] transient upstream failure (attempt %d/%d), retrying in %s: %v",
			attempt+1, len(backoffs)+1, delay, doErr,
		)
		select {
		case <-ctx.Done():
			return nil, doErr
		case <-time.After(delay):
		}
	}
}

// probeUpstreamStatusError drains a non-2xx response (bounded) and builds the
// error handed to the caller and to the retry decision.
func probeUpstreamStatusError(resp *http.Response) error {
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, pelicanProbeMaxContentBytes))
	return &probeHTTPError{
		status:  resp.StatusCode,
		message: fmt.Sprintf("upstream HTTP %d: %s", resp.StatusCode, truncateProbeSnippet(string(raw))),
	}
}

// retryableProbeFailure reports whether a failed attempt is worth another try.
//
// Only transient conditions are retried: 429/5xx and the concurrency gates the
// gateway emits. Configuration errors (400/401/403/404 model_not_found, bad
// parameters) are deterministic and would waste the run budget. A finished
// context is never retried.
func retryableProbeFailure(ctx context.Context, err error) bool {
	if err == nil || ctx.Err() != nil {
		return false
	}
	var statusErr *probeHTTPError
	if errors.As(err, &statusErr) {
		return isTransientUpstreamStatus(statusErr.status, statusErr.message)
	}
	// Transport-level failures (connection reset/refused, EOF) are transient as
	// well; an exhausted context was already handled above.
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	return true
}

// isTransientUpstreamStatus reports whether an upstream status (and body) marks a
// load-related, retryable condition.
func isTransientUpstreamStatus(status int, body string) bool {
	switch status {
	case http.StatusRequestTimeout, http.StatusTooManyRequests,
		http.StatusInternalServerError, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	// Some gateways answer a queue/concurrency rejection with a non-429 status
	// (403/409); the body code is the authoritative signal.
	lower := strings.ToLower(body)
	return strings.Contains(lower, "gateway_concurrency_limit") ||
		strings.Contains(lower, "gateway_queue_full")
}

// readProbeChatStream consumes an OpenAI-compatible chat completion body and
// returns the accumulated assistant content plus the token usage.
//
// Both response shapes are understood: an SSE stream (`data: {...}` lines
// terminated by `data: [DONE]`) and a plain JSON body, which is what a gateway
// returns when it ignores `stream: true`. Nothing is buffered beyond
// pelicanProbeMaxContentBytes: once the cap is hit the reader stops and reports
// truncated=true instead of growing without bound.
func readProbeChatStream(body io.Reader) (content string, promptTokens, completionTokens int, truncated bool, err error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, pelicanProbeStreamBufferBytes), pelicanProbeMaxStreamLineBytes)

	var out strings.Builder
	for scanner.Scan() {
		payload, done := probeStreamPayload(scanner.Text())
		if done {
			// Stop at the terminator instead of waiting for EOF: a gateway may
			// keep the connection open, and blocking there would burn the whole
			// run budget after the answer already arrived.
			return out.String(), promptTokens, completionTokens, false, nil
		}
		if payload == "" {
			continue
		}
		chunk := gjson.Parse(payload)

		// A gateway can report a failure in-band with HTTP 200; surface it as an
		// error instead of silently returning empty content.
		if msg := strings.TrimSpace(chunk.Get("error.message").String()); msg != "" {
			return out.String(), promptTokens, completionTokens, false,
				fmt.Errorf("upstream stream error: %s", truncateProbeSnippet(msg))
		}
		if v := chunk.Get("usage.prompt_tokens"); v.Exists() {
			promptTokens = int(v.Int())
		}
		if v := chunk.Get("usage.completion_tokens"); v.Exists() {
			completionTokens = int(v.Int())
		}

		piece := chunk.Get("choices.0.delta.content").String()
		if piece == "" {
			// Fallback for a non-streaming body (or a gateway that sends the
			// whole answer as a single `message`).
			piece = chunk.Get("choices.0.message.content").String()
		}
		if piece == "" {
			continue
		}
		if out.Len()+len(piece) > pelicanProbeMaxContentBytes {
			_, _ = out.WriteString(piece[:pelicanProbeMaxContentBytes-out.Len()])
			return out.String(), promptTokens, completionTokens, true, nil
		}
		_, _ = out.WriteString(piece)
	}
	if err := scanner.Err(); err != nil {
		return out.String(), promptTokens, completionTokens, false, err
	}
	return out.String(), promptTokens, completionTokens, false, nil
}

// probeStreamPayload extracts the JSON payload out of one stream line and
// reports whether the line is the stream terminator.
//
// Handled framing: SSE `data:` lines (with `data: [DONE]` as the terminator),
// SSE comments/heartbeats (`:`), the non-data SSE fields (`event:`, `id:`,
// `retry:`) and blank separators. A line that is already bare JSON (a gateway
// that ignored `stream: true`) is passed through as-is.
func probeStreamPayload(line string) (payload string, done bool) {
	line = strings.TrimSpace(line)
	switch {
	case line == "":
		return "", false
	case strings.HasPrefix(line, ":"):
		return "", false
	case strings.HasPrefix(line, "data:"):
		line = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
	case strings.HasPrefix(line, "event:"), strings.HasPrefix(line, "id:"), strings.HasPrefix(line, "retry:"):
		return "", false
	}
	if line == "" {
		return "", false
	}
	if line == "[DONE]" {
		return "", true
	}
	if !strings.HasPrefix(line, "{") {
		// Unknown framing (or a truncated trailing line): ignore the line rather
		// than failing the whole run.
		return "", false
	}
	return line, false
}

// truncateProbeSnippet collapses whitespace and caps a snippet embedded in an
// error message.
func truncateProbeSnippet(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > pelicanProbeMaxErrorSnippetBytes {
		return s[:pelicanProbeMaxErrorSnippetBytes]
	}
	return s
}
