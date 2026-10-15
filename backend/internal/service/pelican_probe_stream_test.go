package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// --- SSE helpers ---

// probeDeltaChunk renders one streamed content chunk (the `delta` shape).
func probeDeltaChunk(content string) string {
	b, _ := json.Marshal(map[string]any{
		"choices": []any{map[string]any{"delta": map[string]any{"content": content}}},
	})
	return string(b)
}

// probeMessageChunk renders one chunk in the non-streaming `message` shape.
func probeMessageChunk(content string) string {
	b, _ := json.Marshal(map[string]any{
		"choices": []any{map[string]any{"message": map[string]any{"content": content}}},
	})
	return string(b)
}

// probeUsageChunk renders the trailing usage-only chunk of a stream.
func probeUsageChunk(promptTokens, completionTokens int) string {
	b, _ := json.Marshal(map[string]any{
		"choices": []any{},
		"usage":   map[string]any{"prompt_tokens": promptTokens, "completion_tokens": completionTokens},
	})
	return string(b)
}

// writeProbeSSE writes an OpenAI-compatible SSE stream terminated by [DONE].
func writeProbeSSE(w http.ResponseWriter, chunks ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	flusher, _ := w.(http.Flusher)
	for _, chunk := range chunks {
		_, _ = fmt.Fprintf(w, "data: %s\n\n", chunk)
		if flusher != nil {
			flusher.Flush()
		}
	}
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
}

// errReadPastDone is returned by errorAfterReader once its payload is drained:
// a reader that honors [DONE] never triggers it.
var errReadPastDone = errors.New("read past [DONE]")

// errorAfterReader yields its payload and then fails every further read.
type errorAfterReader struct {
	data   []byte
	offset int
}

func (r *errorAfterReader) Read(p []byte) (int, error) {
	if r.offset >= len(r.data) {
		return 0, errReadPastDone
	}
	n := copy(p, r.data[r.offset:])
	r.offset += n
	return n, nil
}

// --- stream parsing ---

func TestReadProbeChatStream(t *testing.T) {
	t.Run("accumulates delta chunks and usage", func(t *testing.T) {
		body := strings.Join([]string{
			"data: " + probeDeltaChunk("<svg>"),
			"",
			"data: " + probeDeltaChunk("<circle/>"),
			"",
			"",
			"data: " + probeUsageChunk(11, 22),
			"",
			"data: [DONE]",
			"",
		}, "\n")
		content, pt, ct, truncated, err := readProbeChatStream(strings.NewReader(body))
		require.NoError(t, err)
		require.Equal(t, "<svg><circle/>", content)
		require.Equal(t, 11, pt)
		require.Equal(t, 22, ct)
		require.False(t, truncated)
	})

	t.Run("message.content fallback", func(t *testing.T) {
		body := "data: " + probeMessageChunk("whole answer") + "\n\ndata: [DONE]\n\n"
		content, _, _, _, err := readProbeChatStream(strings.NewReader(body))
		require.NoError(t, err)
		require.Equal(t, "whole answer", content)
	})

	t.Run("ignores comments, other sse fields and blank lines", func(t *testing.T) {
		body := strings.Join([]string{
			": keep-alive",
			"event: message",
			"id: 42",
			"retry: 1000",
			"",
			"data: " + probeDeltaChunk("hi"),
			"data: not-json-at-all",
			"data: [DONE]",
		}, "\n")
		content, _, _, _, err := readProbeChatStream(strings.NewReader(body))
		require.NoError(t, err)
		require.Equal(t, "hi", content)
	})

	t.Run("done terminates without waiting for eof", func(t *testing.T) {
		payload := "data: " + probeDeltaChunk("done!") + "\n" +
			"data: [DONE]\n"
		content, _, _, _, err := readProbeChatStream(&errorAfterReader{data: []byte(payload)})
		require.NoError(t, err, "the reader must stop at [DONE], not read until EOF")
		require.Equal(t, "done!", content)
	})

	t.Run("plain json body when the gateway ignores stream", func(t *testing.T) {
		b, _ := json.Marshal(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": "<svg/>"}}},
			"usage":   map[string]any{"prompt_tokens": 3, "completion_tokens": 5},
		})
		content, pt, ct, _, err := readProbeChatStream(strings.NewReader(string(b)))
		require.NoError(t, err)
		require.Equal(t, "<svg/>", content)
		require.Equal(t, 3, pt)
		require.Equal(t, 5, ct)
	})

	t.Run("in-band error payload becomes an error", func(t *testing.T) {
		body := `data: {"error":{"message":"Gateway error: gateway_concurrency_limit"}}` + "\n\n"
		_, _, _, _, err := readProbeChatStream(strings.NewReader(body))
		require.ErrorContains(t, err, "upstream stream error")
		require.ErrorContains(t, err, "gateway_concurrency_limit")
	})

	t.Run("oversized stream is truncated at the cap", func(t *testing.T) {
		const chunkSize = 1 << 20
		var sb strings.Builder
		for i := 0; i < 5; i++ {
			_, _ = sb.WriteString("data: ")
			_, _ = sb.WriteString(probeDeltaChunk(strings.Repeat("a", chunkSize)))
			_, _ = sb.WriteString("\n\n")
		}
		_, _ = sb.WriteString("data: " + probeDeltaChunk("never reached") + "\n\n")
		_, _ = sb.WriteString("data: [DONE]\n\n")

		content, _, _, truncated, err := readProbeChatStream(strings.NewReader(sb.String()))
		require.NoError(t, err)
		require.True(t, truncated)
		require.Len(t, content, pelicanProbeMaxContentBytes)
		require.NotContains(t, content, "never reached")
	})

	t.Run("empty body yields empty content", func(t *testing.T) {
		content, _, _, truncated, err := readProbeChatStream(strings.NewReader(""))
		require.NoError(t, err)
		require.Empty(t, content)
		require.False(t, truncated)
	})
}

func TestExecuteProbeStreamsAndParsesUsage(t *testing.T) {
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &captured)
		writeProbeSSE(w,
			probeDeltaChunk("```html\n<svg>"),
			probeDeltaChunk("pelican</svg>\n```"),
			probeUsageChunk(7, 9),
		)
	}))
	defer srv.Close()

	svc, _, _ := probeSvcWithServer(t, srv)
	raw, pt, ct, latencyMs, err := svc.executeProbe(context.Background(), probeRow(srv.URL))
	require.NoError(t, err)
	require.Equal(t, true, captured["stream"], "the probe must stream: a non-streaming request only answers after the whole generation")
	require.Equal(t, "<svg>pelican</svg>", raw, "streamed code fences are still stripped")
	require.Equal(t, 7, pt)
	require.Equal(t, 9, ct)
	require.GreaterOrEqual(t, latencyMs, int64(0))
}

// --- transient failure retries ---

// probeServiceWithFastRetries builds a service whose retry ladder is negligible.
func probeServiceWithFastRetries(t *testing.T, srv *httptest.Server) *PelicanProbeService {
	t.Helper()
	return &PelicanProbeService{
		planRepo: newFakePelicanPlanRepo(), resultRepo: &fakePelicanResultRepo{},
		encryptor:          fakePelicanEncryptor{},
		httpClient:         srv.Client(),
		retryBackoffLadder: []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond},
	}
}

// flakyProbeServer fails the first failCount requests with status/body, then
// serves a stream.
func flakyProbeServer(t *testing.T, failCount int32, status int, body string, attempts *atomic.Int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) <= failCount {
			w.WriteHeader(status)
			_, _ = io.WriteString(w, body)
			return
		}
		writeProbeSSE(w, probeDeltaChunk("<svg/>"), probeUsageChunk(1, 2))
	}))
}

func TestExecuteProbeRetriesTransientFailures(t *testing.T) {
	concurrencyBody := `{"code":"gateway_concurrency_limit","message":"Concurrency limit exceeded for user"}`

	t.Run("429 concurrency limit then success", func(t *testing.T) {
		var attempts atomic.Int32
		srv := flakyProbeServer(t, 2, http.StatusTooManyRequests, concurrencyBody, &attempts)
		defer srv.Close()

		svc := probeServiceWithFastRetries(t, srv)
		raw, pt, ct, _, err := svc.executeProbe(context.Background(), probeRow(srv.URL))
		require.NoError(t, err)
		require.Equal(t, "<svg/>", raw)
		require.Equal(t, 1, pt)
		require.Equal(t, 2, ct)
		require.EqualValues(t, 3, attempts.Load(), "two transient failures must be retried")
	})

	t.Run("503 queue full then success", func(t *testing.T) {
		var attempts atomic.Int32
		srv := flakyProbeServer(t, 1, http.StatusServiceUnavailable, `{"code":"gateway_queue_full"}`, &attempts)
		defer srv.Close()

		svc := probeServiceWithFastRetries(t, srv)
		_, _, _, _, err := svc.executeProbe(context.Background(), probeRow(srv.URL))
		require.NoError(t, err)
		require.EqualValues(t, 2, attempts.Load())
	})

	t.Run("configuration error is not retried", func(t *testing.T) {
		var attempts atomic.Int32
		srv := flakyProbeServer(t, 99, http.StatusNotFound, `{"error":{"code":"model_not_found"}}`, &attempts)
		defer srv.Close()

		svc := probeServiceWithFastRetries(t, srv)
		_, _, _, _, err := svc.executeProbe(context.Background(), probeRow(srv.URL))
		require.ErrorContains(t, err, "upstream HTTP 404")
		require.ErrorContains(t, err, "model_not_found")
		require.EqualValues(t, 1, attempts.Load(), "4xx configuration errors are deterministic and must not be retried")
	})

	t.Run("bad request is not retried", func(t *testing.T) {
		var attempts atomic.Int32
		srv := flakyProbeServer(t, 99, http.StatusBadRequest, `{"error":{"message":"invalid parameter"}}`, &attempts)
		defer srv.Close()

		svc := probeServiceWithFastRetries(t, srv)
		_, _, _, _, err := svc.executeProbe(context.Background(), probeRow(srv.URL))
		require.ErrorContains(t, err, "upstream HTTP 400")
		require.EqualValues(t, 1, attempts.Load())
	})

	t.Run("persistent 429 stops after the retry budget", func(t *testing.T) {
		var attempts atomic.Int32
		srv := flakyProbeServer(t, 99, http.StatusTooManyRequests, concurrencyBody, &attempts)
		defer srv.Close()

		svc := probeServiceWithFastRetries(t, srv)
		_, _, _, _, err := svc.executeProbe(context.Background(), probeRow(srv.URL))
		require.ErrorContains(t, err, "upstream HTTP 429")
		require.EqualValues(t, 4, attempts.Load(), "one attempt plus the three configured retries")
	})

	t.Run("cancelled context is not retried", func(t *testing.T) {
		var attempts atomic.Int32
		srv := flakyProbeServer(t, 99, http.StatusTooManyRequests, concurrencyBody, &attempts)
		defer srv.Close()

		svc := probeServiceWithFastRetries(t, srv)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, _, _, _, err := svc.executeProbe(ctx, probeRow(srv.URL))
		require.Error(t, err)
		require.LessOrEqual(t, attempts.Load(), int32(1), "a cancelled context must not start new attempts")
	})
}

func TestIsTransientUpstreamStatus(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{name: "429", status: http.StatusTooManyRequests, want: true},
		{name: "503", status: http.StatusServiceUnavailable, want: true},
		{name: "500", status: http.StatusInternalServerError, want: true},
		{name: "502", status: http.StatusBadGateway, want: true},
		{name: "504", status: http.StatusGatewayTimeout, want: true},
		{name: "408", status: http.StatusRequestTimeout, want: true},
		{name: "403 with concurrency code", status: http.StatusForbidden, body: `{"code":"gateway_concurrency_limit"}`, want: true},
		{name: "409 with queue full code", status: http.StatusConflict, body: `{"code":"gateway_queue_full"}`, want: true},
		{name: "404 model_not_found", status: http.StatusNotFound, body: `{"error":{"code":"model_not_found"}}`, want: false},
		{name: "400 invalid request", status: http.StatusBadRequest, body: `{"error":{"message":"bad param"}}`, want: false},
		{name: "401 unauthorized", status: http.StatusUnauthorized, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, isTransientUpstreamStatus(tc.status, tc.body))
		})
	}
}
