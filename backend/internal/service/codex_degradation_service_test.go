package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type capturedCodexProbeRequest struct {
	method        string
	host          string
	authorization string
	body          []byte
	header        http.Header
}

// TestCodexProbeNativeDiagnosticUsesAccountToken 覆盖号原生探测：请求由目标
// 账号自身的 OAuth 凭据发出，不经过公开网关、不携带任何用户 API Key。
func TestCodexProbeNativeDiagnosticUsesAccountToken(t *testing.T) {
	const model = "gpt-6.1-sol"
	const streamed = "73 12 91 4"
	captured := make(chan capturedCodexProbeRequest, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		captured <- capturedCodexProbeRequest{
			method:        r.Method,
			host:          r.Host,
			authorization: r.Header.Get("Authorization"),
			body:          body,
			header:        r.Header.Clone(),
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(
			"data: {\"type\":\"response.created\"}\n\n" +
				"data: {\"type\":\"response.output_text.delta\",\"delta\":\"73 12 \"}\n\n" +
				"data: {\"type\":\"response.output_text.delta\",\"delta\":\"91 4\"}\n\n" +
				"data: {\"type\":\"response.completed\",\"response\":{\"output\":[{\"content\":[{\"type\":\"output_text\",\"text\":\"73 12 91 4\"}]}]}}\n\n",
		))
	}))
	defer server.Close()

	account := codexProbeTestAccount()
	repo := newCodexDegradationRepoStub(account)
	svc := newCodexDegradationTestService(repo, &upstreamBillingProbeSettingRepo{}, CodexDegradationDeps{
		HTTPClient: server.Client(),
		Endpoint:   server.URL,
	})
	challenge := ModelTraceChallenge{Prompt: "native diagnostic challenge", ExpectedCount: 4}

	result := svc.RunNativeDiagnostic(context.Background(), account.ID, model, challenge, 2*time.Second)

	require.Empty(t, result.Reason)
	require.Equal(t, http.StatusOK, result.HTTPStatus)
	require.Equal(t, streamed, result.Output)

	request := <-captured
	require.Equal(t, http.MethodPost, request.method)
	require.Equal(t, "chatgpt.com", request.host)
	require.Equal(t, "Bearer "+account.GetOpenAIAccessToken(), request.authorization)
	require.NotEqual(t, "Bearer sk-user-key-native-probe-must-not-use", request.authorization)
	require.Equal(t, model, gjson.GetBytes(request.body, "model").String())
	require.Contains(t, string(request.body), challenge.Prompt)
	assertCodexProbeIdentity(t, request.body, request.header)
	require.Equal(t, "acc-41", request.header.Get("chatgpt-account-id"))
}

func TestCodexProbeNativeDiagnosticMissingAccountToken(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	account := codexProbeTestAccount()
	account.Credentials = map[string]any{"chatgpt_account_id": "acc-41"}
	repo := newCodexDegradationRepoStub(account)
	svc := newCodexDegradationTestService(repo, &upstreamBillingProbeSettingRepo{}, CodexDegradationDeps{
		HTTPClient: server.Client(),
		Endpoint:   server.URL,
		Tokens:     codexDegradationTokenStub{},
	})

	result := svc.RunNativeDiagnostic(context.Background(), account.ID, "gpt-6.1-sol",
		ModelTraceChallenge{Prompt: "challenge", ExpectedCount: 4}, 2*time.Second)

	require.Equal(t, "credentials", result.Reason)
	require.Equal(t, 0, calls, "credential failure must not reach upstream")
	require.Equal(t, 0, result.HTTPStatus)
}

func TestCodexProbeNativeDiagnosticHTTP503(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("service unavailable"))
	}))
	defer server.Close()

	account := codexProbeTestAccount()
	svc := newCodexDegradationTestService(newCodexDegradationRepoStub(account), &upstreamBillingProbeSettingRepo{}, CodexDegradationDeps{
		HTTPClient: server.Client(),
		Endpoint:   server.URL,
	})

	result := svc.RunNativeDiagnostic(context.Background(), account.ID, "gpt-6.1-sol",
		ModelTraceChallenge{Prompt: "challenge", ExpectedCount: 4}, 2*time.Second)

	require.Equal(t, "http_status", result.Reason)
	require.Equal(t, http.StatusServiceUnavailable, result.HTTPStatus)
	require.Empty(t, result.Output)
}

func TestCodexProbeNativeDiagnosticEmptyResponseIncomplete(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.created\"}\n\n"))
	}))
	defer server.Close()

	account := codexProbeTestAccount()
	svc := newCodexDegradationTestService(newCodexDegradationRepoStub(account), &upstreamBillingProbeSettingRepo{}, CodexDegradationDeps{
		HTTPClient: server.Client(),
		Endpoint:   server.URL,
	})

	result := svc.RunNativeDiagnostic(context.Background(), account.ID, "gpt-6.1-sol",
		ModelTraceChallenge{Prompt: "challenge", ExpectedCount: 4}, 2*time.Second)

	require.Equal(t, "response_incomplete", result.Reason)
	require.Equal(t, http.StatusOK, result.HTTPStatus)
	require.Empty(t, result.Output)
}

func TestCodexProbeNativeDiagnosticTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"1 2\"}\n\n"))
	}))
	defer server.Close()

	account := codexProbeTestAccount()
	svc := newCodexDegradationTestService(newCodexDegradationRepoStub(account), &upstreamBillingProbeSettingRepo{}, CodexDegradationDeps{
		HTTPClient: server.Client(),
		Endpoint:   server.URL,
	})

	result := svc.RunNativeDiagnostic(context.Background(), account.ID, "gpt-6.1-sol",
		ModelTraceChallenge{Prompt: "challenge", ExpectedCount: 4}, 50*time.Millisecond)

	require.Equal(t, "timeout", result.Reason)
	require.Equal(t, 0, result.HTTPStatus)
}

func TestCodexProbeNativeDiagnosticUnknownAccount(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	account := codexProbeTestAccount()
	svc := newCodexDegradationTestService(newCodexDegradationRepoStub(account), &upstreamBillingProbeSettingRepo{}, CodexDegradationDeps{
		HTTPClient: server.Client(),
		Endpoint:   server.URL,
	})

	result := svc.RunNativeDiagnostic(context.Background(), account.ID+1000, "gpt-6.1-sol",
		ModelTraceChallenge{Prompt: "challenge", ExpectedCount: 4}, 2*time.Second)

	require.Equal(t, "identity_resolution_failed", result.Reason)
	require.Equal(t, 0, calls, "unknown account must not reach upstream")
	require.Equal(t, 0, result.HTTPStatus)
}

// TestCodexProbeNativeDiagnosticJSONFallback 兼容非 SSE 的 Responses JSON 响应。
func TestCodexProbeNativeDiagnosticJSONFallback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"output":[{"content":[{"type":"output_text","text":"5 6 7"}]}]}`))
	}))
	defer server.Close()

	account := codexProbeTestAccount()
	svc := newCodexDegradationTestService(newCodexDegradationRepoStub(account), &upstreamBillingProbeSettingRepo{}, CodexDegradationDeps{
		HTTPClient: server.Client(),
		Endpoint:   server.URL,
	})

	result := svc.RunNativeDiagnostic(context.Background(), account.ID, "gpt-6.1-sol",
		ModelTraceChallenge{Prompt: "challenge", ExpectedCount: 4}, 2*time.Second)
	require.Empty(t, result.Reason)
	require.Equal(t, "5 6 7", result.Output)
}

// TestCodexDegradationCheckAccountAggregatesVerdicts 覆盖手动检测的四态汇总。
func TestCodexDegradationCheckAccountAggregatesVerdicts(t *testing.T) {
	account := codexProbeTestAccount()
	repo := newCodexDegradationRepoStub(account)
	svc := newCodexDegradationTestService(repo, &upstreamBillingProbeSettingRepo{}, CodexDegradationDeps{})
	svc.probe = &codexDegradationProbeStub{next: func(_ int64, model string) (CodexDiagnosticVerdict, error) {
		switch model {
		case "gpt-6-sol":
			return CodexDiagnosticVerdict{Status: CodexDegradationStatusNormal, PredictedModel: "gpt-6-sol", Probability: 0.99, ParsedCount: 300}, nil
		case "gpt-5.6-sol":
			return CodexDiagnosticVerdict{Status: CodexDegradationStatusUncertain, Reason: "reference_class_unavailable"}, nil
		default:
			return CodexDiagnosticVerdict{Status: CodexDegradationStatusFailed, Reason: "timeout"}, nil
		}
	}}

	result, err := svc.CheckAccount(context.Background(), account.ID, []string{"gpt-6-sol", "gpt-5.6-sol", "gpt-6-astra"})
	require.NoError(t, err)
	require.Equal(t, CodexDegradationStatusFailed, result.Status, "any failed verdict fails the batch")
	require.Len(t, result.Items, 3)
	require.Equal(t, CodexDegradationStatusNormal, result.Items[0].Status)
	require.Equal(t, CodexDegradationStatusUncertain, result.Items[1].Status)

	result, err = svc.CheckAccount(context.Background(), account.ID, []string{"gpt-6-sol", "gpt-5.6-sol"})
	require.NoError(t, err)
	require.Equal(t, CodexDegradationStatusUncertain, result.Status)

	_, err = svc.CheckAccount(context.Background(), account.ID, []string{"gpt-does-not-exist"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown diagnostic model")
}
