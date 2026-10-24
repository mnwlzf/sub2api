package service

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestCodexProbeDefaultStructureAndPrivacy(t *testing.T) {
	raw := DefaultCodexProbeTemplate()
	template, err := ParseCodexProbeTemplate(raw)
	require.NoError(t, err)
	require.Len(t, strings.Split(strings.TrimSpace(raw), "\n"), 7)
	for _, char := range raw {
		require.Less(t, char, rune(128), "default fixed instructions must be English")
	}
	require.NotRegexp(t, regexp.MustCompile(`[A-Za-z]:[\\/]|/(?:mnt|home|Users)/|[\w.+-]+@[\w.-]+\.[A-Za-z]{2,}|[0-9a-f]{8}-(?:[0-9a-f]{4}-){3}[0-9a-f]{12}`), raw)
	require.Len(t, template.messages, 5)
	for i, role := range []string{"developer", "developer", "developer", "user", "user"} {
		require.Equal(t, role, template.messages[i].Role)
		require.Len(t, template.messages[i].Content, []int{5, 1, 1, 1, 1}[i])
	}
	decoder := xml.NewDecoder(strings.NewReader(template.messages[3].Content[0].Text))
	counts := map[string]int{}
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		if start, ok := token.(xml.StartElement); ok {
			counts[start.Name.Local]++
		}
	}
	require.Equal(t, 3, counts["root"])
	require.Equal(t, 18, counts["entry"])
	require.Contains(t, template.messages[0].Content[3].Text, "<permissions instructions>")
}

func TestCodexProbeTemplateRejectsInvalidOrPrivateRecords(t *testing.T) {
	good := DefaultCodexProbeTemplate()
	for name, raw := range map[string]string{
		"malformed":             "{private-secret",
		"too large":             strings.Repeat(" ", CodexProbeTemplateMaxBytes+1),
		"recording metadata":    strings.Replace(good, `"originator":`, `"cwd":"/home/private-user","originator":`, 1),
		"assistant":             strings.Replace(good, `"role":"developer"`, `"role":"assistant"`, 1),
		"output text":           strings.Replace(good, `"input_text"`, `"output_text"`, 1),
		"unknown placeholder":   strings.Replace(good, "anonymous workspace", "{{PRIVATE_VALUE}}", 1),
		"malformed placeholder": strings.Replace(good, "anonymous workspace", "{{PRIVATE_VALUE", 1),
		"duplicate prompt":      strings.Replace(good, "anonymous workspace", CodexProbePromptPlaceholder, 1),
		"missing prompt":        strings.ReplaceAll(good, CodexProbePromptPlaceholder, "private-secret"),
		"fixed timezone":        strings.ReplaceAll(good, "{{TIMEZONE}}", "Europe/London"),
		"missing tag":           strings.Replace(good, "</skills_instructions>", "", 1),
		"changed tree":          strings.Replace(good, "<root>/workspace</root>", "", 1),
		"extra event":           good + `{"type":"response_item","payload":{"type":"reasoning","encrypted_content":"private-secret"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ParseCodexProbeTemplate(raw)
			require.ErrorIs(t, err, ErrCodexProbeTemplateInvalid)
			require.Contains(t, err.Error(), "line ")
			require.NotContains(t, err.Error(), "private-secret")
			require.NotContains(t, err.Error(), "private-user")
		})
	}
}

func TestCodexProbeRenderingTimezoneAndEscaping(t *testing.T) {
	template, err := ParseCodexProbeTemplate(strings.Replace(DefaultCodexProbeTemplate(), "You are Codex, an AI assistant.", "Target: {{MODEL}}.", 1))
	require.NoError(t, err)
	identity, err := newCodexProbeIdentity(uuid.NewString(), time.Date(2026, 9, 24, 0, 30, 0, 0, time.UTC))
	require.NoError(t, err)
	prompt := "quoted \"value\"\n中文 \\ {{TIMEZONE}}"
	for zone, date := range map[string]string{"America/Los_Angeles": "2026-09-23", "Asia/Singapore": "2026-09-24"} {
		body, headers, err := template.renderCodexProbeRequest("gpt-6-astra", prompt, zone, identity)
		require.NoError(t, err)
		require.True(t, json.Valid(body))
		require.Equal(t, prompt, gjson.GetBytes(body, "input.4.content.0.text").String(), "replacement values must not be expanded recursively")
		env := gjson.GetBytes(body, "input.3.content.0.text").String()
		require.Contains(t, env, "<timezone>"+zone+"</timezone>")
		require.Contains(t, env, "<current_date>"+date+"</current_date>")
		require.Contains(t, gjson.GetBytes(body, "instructions").String(), "Target: gpt-6-astra.")
		assertCodexProbeIdentity(t, body, headers)
	}
	require.Contains(t, template.messages[3].Content[0].Text, "{{TIMEZONE}}", "cached template must remain immutable")
}

func assertCodexProbeIdentity(t *testing.T, body []byte, headers http.Header) {
	t.Helper()
	session := headers.Get("session_id")
	for _, name := range []string{"session-id", "thread-id", "x-client-request-id"} {
		require.Equal(t, session, headers.Get(name))
	}
	for _, path := range []string{"prompt_cache_key", "client_metadata.session_id", "client_metadata.thread_id"} {
		require.Equal(t, session, gjson.GetBytes(body, path).String())
	}
	metadata := headers.Get("x-codex-turn-metadata")
	require.Equal(t, metadata, gjson.GetBytes(body, "client_metadata.x-codex-turn-metadata").String())
	require.Equal(t, session, gjson.Get(metadata, "session_id").String())
	require.Equal(t, session, gjson.Get(metadata, "thread_id").String())
	for _, pair := range [][2]string{{"installation_id", "x-codex-installation-id"}, {"window_id", "x-codex-window-id"}} {
		require.Equal(t, headers.Get(pair[1]), gjson.Get(metadata, pair[0]).String())
		require.Equal(t, headers.Get(pair[1]), gjson.GetBytes(body, "client_metadata."+pair[1]).String())
	}
	require.Equal(t, gjson.Get(metadata, "turn_id").String(), gjson.GetBytes(body, "client_metadata.turn_id").String())
	require.Positive(t, gjson.Get(metadata, "turn_started_at_unix_ms").Int())
	require.Equal(t, "turn", gjson.Get(metadata, "request_kind").String())
	for _, value := range []string{session, gjson.Get(metadata, "turn_id").String()} {
		id, err := uuid.Parse(value)
		require.NoError(t, err)
		require.Equal(t, uuid.Version(7), id.Version())
	}
	// 官方 window_id 是 <thread-uuid>:<ordinal>，不是裸 UUID。
	windowParts := strings.SplitN(headers.Get("x-codex-window-id"), ":", 2)
	require.Len(t, windowParts, 2)
	require.Equal(t, "0", windowParts[1])
	windowThread, err := uuid.Parse(windowParts[0])
	require.NoError(t, err)
	require.Equal(t, uuid.Version(7), windowThread.Version())
	id, err := uuid.Parse(headers.Get("x-codex-installation-id"))
	require.NoError(t, err)
	require.Equal(t, uuid.Version(4), id.Version())
}

func TestCodexProbeAccountIdentityAndDefaultTimezone(t *testing.T) {
	account := codexProbeTestAccount()
	challenge := ModelTraceChallenge{Prompt: "test prompt", ExpectedCount: 292}
	template, err := parsedDefaultCodexProbeTemplate()
	require.NoError(t, err)
	first, err := newCodexProbeIdentityForAccount(account, time.Now())
	require.NoError(t, err)
	second, err := newCodexProbeIdentityForAccount(account, time.Now())
	require.NoError(t, err)
	require.Equal(t, first.installationID, second.installationID, "installation id is account-scoped and stable")
	require.NotEqual(t, first.sessionID, second.sessionID)
	require.Equal(t, defaultCodexProbeTimezone, codexProbeTimezoneForAccount(account))

	body, headers, err := template.renderCodexProbeRequest("gpt-6-astra", challenge.Prompt, codexProbeTimezoneForAccount(account), first)
	require.NoError(t, err)
	assertCodexProbeIdentity(t, body, headers)
	require.Contains(t, gjson.GetBytes(body, "input.3.content.0.text").String(), "<timezone>Asia/Singapore</timezone>")

	other := codexProbeTestAccount()
	other.Credentials["chatgpt_account_id"] = "another-test-account"
	third, err := newCodexProbeIdentityForAccount(other, time.Now())
	require.NoError(t, err)
	require.NotEqual(t, first.installationID, third.installationID)

	other.Credentials = map[string]any{"access_token": "tok"}
	_, err = newCodexProbeIdentityForAccount(other, time.Now())
	require.ErrorIs(t, err, ErrCodexProbeIdentity)
}

// TestCodexProbeInvalidTemplateNeverReachesUpstream 覆盖"模板非法时拒绝发请求"。
func TestCodexProbeInvalidTemplateNeverReachesUpstream(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"1 2 3\"}\n\n"))
	}))
	defer server.Close()

	settingsRepo := &upstreamBillingProbeSettingRepo{values: map[string]string{
		SettingKeyCodexProbeTemplate: "{private-secret",
	}}
	repo := newCodexDegradationRepoStub(codexProbeTestAccount())
	svc := newCodexDegradationTestService(repo, settingsRepo, CodexDegradationDeps{
		HTTPClient: server.Client(),
		Endpoint:   server.URL,
	})

	result := svc.RunNativeDiagnostic(context.Background(), 41, "gpt-6.1-sol",
		ModelTraceChallenge{Prompt: "challenge", ExpectedCount: 4}, 2*time.Second)
	require.Equal(t, "template_invalid", result.Reason)
	require.Equal(t, int64(0), calls.Load(), "invalid template must not reach upstream")
	require.NotContains(t, result.Reason, "private-secret")
}
