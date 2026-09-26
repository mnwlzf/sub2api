package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// poolUpstreamInfoRepo stubs the two optional repository capabilities the
// probe uses (due listing + CAS snapshot write). The write simulates the
// repository compare-and-swap: the account's current platform/type,
// credentials, proxy, selection keys, and previous snapshot must match the
// expected account loaded before the network call.
type poolUpstreamInfoRepo struct {
	*upstreamBillingProbeAccountRepo
	due               []Account
	mutateBeforeWrite func()
}

func (r *poolUpstreamInfoRepo) ListDuePoolUpstreamInfoAccounts(_ context.Context, _ time.Time, limit int) ([]Account, error) {
	out := append([]Account(nil), r.due...)
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (r *poolUpstreamInfoRepo) UpdatePoolUpstreamInfoSnapshot(_ context.Context, expected *Account, snapshot *PoolUpstreamInfoSnapshot) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.mutateBeforeWrite != nil {
		r.mutateBeforeWrite()
	}
	account := r.accounts[expected.ID]
	if account == nil ||
		account.Platform != expected.Platform ||
		account.Type != expected.Type ||
		!reflect.DeepEqual(account.Credentials, expected.Credentials) ||
		!reflect.DeepEqual(account.ProxyID, expected.ProxyID) {
		return ErrPoolUpstreamInfoIdentityChanged
	}
	if !poolExtraValueEqual(account.Extra[PoolUpstreamPlatformExtraKey], expected.Extra[PoolUpstreamPlatformExtraKey]) ||
		!poolExtraValueEqual(account.Extra[PoolUpstreamFeaturesExtraKey], expected.Extra[PoolUpstreamFeaturesExtraKey]) ||
		!poolExtraValueEqual(account.Extra[PoolUpstreamInfoExtraKey], expected.Extra[PoolUpstreamInfoExtraKey]) {
		return ErrPoolUpstreamInfoIdentityChanged
	}
	if account.Extra == nil {
		account.Extra = make(map[string]any)
	}
	account.Extra[PoolUpstreamInfoExtraKey] = snapshot
	return nil
}

func poolExtraValueEqual(left, right any) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	lb, lerr := json.Marshal(left)
	rb, rerr := json.Marshal(right)
	return lerr == nil && rerr == nil && string(lb) == string(rb)
}

func poolInfoResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func newPoolUpstreamInfoAccount(id int64, platform, upstreamPlatform string, features []string) *Account {
	extra := map[string]any{}
	if upstreamPlatform != "" {
		extra[PoolUpstreamPlatformExtraKey] = upstreamPlatform
		extra[PoolUpstreamFeaturesExtraKey] = features
	}
	return &Account{
		ID:          id,
		Platform:    platform,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":   "sk-pool-info",
			"base_url":  "https://pool.example",
			"pool_mode": true,
		},
		Extra: extra,
	}
}

func TestPoolUpstreamInfoEndpointURLMatrix(t *testing.T) {
	cases := []struct {
		base     string
		platform string
		want     string
		wantErr  bool
	}{
		{"https://pool.example", PoolUpstreamPlatformSub2API, "https://pool.example/v1/usage", false},
		{"https://pool.example/", PoolUpstreamPlatformSub2API, "https://pool.example/v1/usage", false},
		{"https://pool.example/v1", PoolUpstreamPlatformSub2API, "https://pool.example/v1/usage", false},
		{"https://pool.example/api/v1", PoolUpstreamPlatformSub2API, "https://pool.example/v1/usage", false},
		{"https://pool.example/proxy", PoolUpstreamPlatformChatGPT2API, "https://pool.example/proxy/api/dashboard", false},
		{"https://pool.example/proxy/v1", PoolUpstreamPlatformChatGPT2API, "https://pool.example/proxy/api/dashboard", false},
		{"https://pool.example/proxy/api/v1", PoolUpstreamPlatformChatGPT2API, "https://pool.example/proxy/api/dashboard", false},
		{"https://user:pw@pool.example/v1?x=1#frag", PoolUpstreamPlatformSub2API, "https://pool.example/v1/usage", false},
		{"https://pool.example", PoolUpstreamPlatformDefault, "", true},
		{"://bad", PoolUpstreamPlatformSub2API, "", true},
	}
	for _, tc := range cases {
		got, err := poolUpstreamInfoEndpointURL(tc.base, tc.platform)
		if tc.wantErr {
			require.Error(t, err, "base=%q platform=%q", tc.base, tc.platform)
			continue
		}
		require.NoError(t, err, "base=%q platform=%q", tc.base, tc.platform)
		require.Equal(t, tc.want, got, "base=%q platform=%q", tc.base, tc.platform)
	}
}

func TestParsePoolUpstreamSub2APIUsageMatrix(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		wantKind   string
		wantAmount float64
		wantErr    bool
	}{
		{"wallet", `{"mode":"unrestricted","balance":12.5,"remaining":12.5,"unit":"USD"}`, "wallet", 12.5, false},
		{"quota", `{"mode":"quota_limited","remaining":7.25,"unit":"USD"}`, "quota", 7.25, false},
		{"subscription", `{"mode":"unrestricted","remaining":3.5,"unit":"USD","subscription":{"daily_usage_usd":1}}`, "subscription", 3.5, false},
		{"unrestricted remaining without balance or subscription fails", `{"mode":"unrestricted","remaining":8,"unit":"USD"}`, "", 0, true},
		{"unrestricted non-object subscription fails", `{"mode":"unrestricted","remaining":8,"unit":"USD","subscription":"none"}`, "", 0, true},
		{"unknown mode with numeric remaining fails", `{"mode":"mystery","remaining":5,"unit":"USD"}`, "", 0, true},
		{"invalid balance is an error not a fallback", `{"mode":"unrestricted","balance":-1,"remaining":5,"unit":"USD"}`, "", 0, true},
		{"non numeric balance", `{"mode":"unrestricted","balance":"many","unit":"USD"}`, "", 0, true},
		{"missing remaining", `{"mode":"quota_limited","unit":"USD"}`, "", 0, true},
		{"wrong unit", `{"mode":"quota_limited","remaining":5,"unit":"POINTS"}`, "", 0, true},
		{"missing unit", `{"mode":"quota_limited","remaining":5}`, "", 0, true},
		{"string remaining", `{"mode":"quota_limited","remaining":"abc","unit":"USD"}`, "", 0, true},
		{"negative remaining", `{"mode":"quota_limited","remaining":-0.5,"unit":"USD"}`, "", 0, true},
		{"malformed json", `not json`, "", 0, true},
		{"trailing json rejected", `{"mode":"quota_limited","remaining":5,"unit":"USD"} {}`, "", 0, true},
		{"empty object", `{}`, "", 0, true},
	}
	for _, tc := range cases {
		data, err := parsePoolUpstreamSub2APIUsage([]byte(tc.body))
		if tc.wantErr {
			require.Error(t, err, tc.name)
			continue
		}
		require.NoError(t, err, tc.name)
		require.Equal(t, tc.wantKind, data["kind"], tc.name)
		require.Equal(t, tc.wantAmount, data["amount_usd"], tc.name)
	}
}

func TestParsePoolUpstreamChatGPT2APIDashboardMatrix(t *testing.T) {
	data, err := parsePoolUpstreamChatGPT2APIDashboard(
		[]byte(`{"accounts":{"total":1,"active":1,"total_quota":25,"unlimited_quota_count":0,"unknown_quota_count":0}}`),
		[]string{PoolUpstreamFeatureAccountCount, PoolUpstreamFeatureImageQuota},
	)
	require.NoError(t, err)
	require.Equal(t, int64(1), data["accounts_active"])
	require.Equal(t, int64(25), data["total_quota"])
	require.Equal(t, int64(0), data["unlimited_quota_count"])
	require.Equal(t, int64(0), data["unknown_quota_count"])

	// Unselected fields are never projected, including the optional counters
	// that only accompany image_quota.
	data, err = parsePoolUpstreamChatGPT2APIDashboard(
		[]byte(`{"accounts":{"active":3,"total_quota":25,"unlimited_quota_count":2,"unknown_quota_count":1}}`),
		[]string{PoolUpstreamFeatureAccountCount},
	)
	require.NoError(t, err)
	require.Equal(t, int64(3), data["accounts_active"])
	_, hasQuota := data["total_quota"]
	require.False(t, hasQuota)
	_, hasUnlimited := data["unlimited_quota_count"]
	require.False(t, hasUnlimited)
	_, hasUnknown := data["unknown_quota_count"]
	require.False(t, hasUnknown)

	_, err = parsePoolUpstreamChatGPT2APIDashboard(
		[]byte(`{"accounts":{"active":3}}`),
		[]string{PoolUpstreamFeatureImageQuota},
	)
	require.Error(t, err)

	_, err = parsePoolUpstreamChatGPT2APIDashboard(
		[]byte(`{"accounts":{"total_quota":7}}`),
		[]string{PoolUpstreamFeatureAccountCount},
	)
	require.Error(t, err)

	_, err = parsePoolUpstreamChatGPT2APIDashboard(
		[]byte(`{"accounts":{"active":1.5}}`),
		[]string{PoolUpstreamFeatureAccountCount},
	)
	require.Error(t, err)

	_, err = parsePoolUpstreamChatGPT2APIDashboard(
		[]byte(`{"accounts":{"active":-2,"total_quota":25}}`),
		[]string{PoolUpstreamFeatureAccountCount},
	)
	require.Error(t, err)

	_, err = parsePoolUpstreamChatGPT2APIDashboard(
		[]byte(`{"other":1}`),
		[]string{PoolUpstreamFeatureAccountCount},
	)
	require.Error(t, err)
}

func TestExtractPoolUpstreamSelectionUpdateValidation(t *testing.T) {
	update, err := extractPoolUpstreamSelectionUpdate(nil)
	require.NoError(t, err)
	require.False(t, update.provided())

	extra := map[string]any{
		PoolUpstreamPlatformExtraKey: PoolUpstreamPlatformSub2API,
		PoolUpstreamFeaturesExtraKey: []any{"balance"},
		PoolUpstreamInfoExtraKey:     map[string]any{"status": "ok"},
	}
	update, err = extractPoolUpstreamSelectionUpdate(extra)
	require.NoError(t, err)
	require.True(t, update.provided())
	require.Equal(t, PoolUpstreamPlatformSub2API, update.platform)
	require.Equal(t, []string{PoolUpstreamFeatureBalance}, update.features)
	// The probe-managed snapshot key is always dropped from client payloads.
	_, present := extra[PoolUpstreamInfoExtraKey]
	require.False(t, present)
	_, present = extra[PoolUpstreamPlatformExtraKey]
	require.False(t, present)

	_, err = extractPoolUpstreamSelectionUpdate(map[string]any{PoolUpstreamPlatformExtraKey: "bogus"})
	require.ErrorIs(t, err, ErrPoolUpstreamInfoInvalidPlatform)

	_, err = extractPoolUpstreamSelectionUpdate(map[string]any{PoolUpstreamFeaturesExtraKey: "balance"})
	require.ErrorIs(t, err, ErrPoolUpstreamInfoInvalidFeatures)

	_, err = extractPoolUpstreamSelectionUpdate(map[string]any{PoolUpstreamFeaturesExtraKey: []any{"wat"}})
	require.ErrorIs(t, err, ErrPoolUpstreamInfoInvalidFeatures)
}

func TestResolvePoolUpstreamSelectionRules(t *testing.T) {
	eligibleAnthropic := newPoolUpstreamInfoAccount(1, PlatformAnthropic, "", nil)
	eligibleOpenAI := newPoolUpstreamInfoAccount(2, PlatformOpenAI, "", nil)
	notPoolMode := newPoolUpstreamInfoAccount(3, PlatformOpenAI, "", nil)
	notPoolMode.Credentials["pool_mode"] = false
	oauth := newPoolUpstreamInfoAccount(4, PlatformOpenAI, "", nil)
	oauth.Type = AccountTypeOAuth

	platform, features, err := resolvePoolUpstreamSelection(eligibleAnthropic, poolUpstreamSelectionUpdate{
		platformProvided: true, platform: PoolUpstreamPlatformSub2API,
		featuresProvided: true, features: []string{PoolUpstreamFeatureBalance},
	}, nil)
	require.NoError(t, err)
	require.Equal(t, PoolUpstreamPlatformSub2API, platform)
	require.Equal(t, []string{PoolUpstreamFeatureBalance}, features)

	// chatgpt2api requires an OpenAI API-key account.
	_, _, err = resolvePoolUpstreamSelection(eligibleAnthropic, poolUpstreamSelectionUpdate{
		platformProvided: true, platform: PoolUpstreamPlatformChatGPT2API,
		featuresProvided: true, features: []string{PoolUpstreamFeatureAccountCount},
	}, nil)
	require.ErrorIs(t, err, ErrPoolUpstreamInfoInvalidCombination)

	platform, features, err = resolvePoolUpstreamSelection(eligibleOpenAI, poolUpstreamSelectionUpdate{
		platformProvided: true, platform: PoolUpstreamPlatformChatGPT2API,
		featuresProvided: true, features: []string{PoolUpstreamFeatureAccountCount, PoolUpstreamFeatureImageQuota},
	}, nil)
	require.NoError(t, err)
	require.Equal(t, PoolUpstreamPlatformChatGPT2API, platform)
	require.Equal(t, []string{PoolUpstreamFeatureAccountCount, PoolUpstreamFeatureImageQuota}, features)

	// Switching platform without an explicit feature list drops incompatible features.
	stored := map[string]any{
		PoolUpstreamPlatformExtraKey: PoolUpstreamPlatformSub2API,
		PoolUpstreamFeaturesExtraKey: []string{PoolUpstreamFeatureBalance},
	}
	platform, features, err = resolvePoolUpstreamSelection(eligibleOpenAI, poolUpstreamSelectionUpdate{
		platformProvided: true, platform: PoolUpstreamPlatformChatGPT2API,
	}, stored)
	require.NoError(t, err)
	require.Equal(t, PoolUpstreamPlatformChatGPT2API, platform)
	require.Empty(t, features)

	// Providing a selection on a non-pool-mode or non-apikey account is rejected.
	_, _, err = resolvePoolUpstreamSelection(notPoolMode, poolUpstreamSelectionUpdate{
		platformProvided: true, platform: PoolUpstreamPlatformSub2API,
		featuresProvided: true, features: []string{PoolUpstreamFeatureBalance},
	}, nil)
	require.ErrorIs(t, err, ErrPoolUpstreamInfoAccountInvalid)

	_, _, err = resolvePoolUpstreamSelection(oauth, poolUpstreamSelectionUpdate{
		platformProvided: true, platform: PoolUpstreamPlatformSub2API,
		featuresProvided: true, features: []string{PoolUpstreamFeatureBalance},
	}, nil)
	require.ErrorIs(t, err, ErrPoolUpstreamInfoAccountInvalid)

	// A stored selection normalizes to default+[] once the account becomes ineligible.
	platform, features, err = resolvePoolUpstreamSelection(notPoolMode, poolUpstreamSelectionUpdate{}, stored)
	require.NoError(t, err)
	require.Equal(t, PoolUpstreamPlatformDefault, platform)
	require.Empty(t, features)

	// Default platform with features is invalid.
	_, _, err = resolvePoolUpstreamSelection(eligibleOpenAI, poolUpstreamSelectionUpdate{
		platformProvided: true, platform: PoolUpstreamPlatformDefault,
		featuresProvided: true, features: []string{PoolUpstreamFeatureBalance},
	}, nil)
	require.ErrorIs(t, err, ErrPoolUpstreamInfoInvalidCombination)
}

func TestProbePoolUpstreamInfoSub2APISuccess(t *testing.T) {
	account := newPoolUpstreamInfoAccount(1, PlatformAnthropic, PoolUpstreamPlatformSub2API, []string{PoolUpstreamFeatureBalance})
	repo := &poolUpstreamInfoRepo{upstreamBillingProbeAccountRepo: &upstreamBillingProbeAccountRepo{
		accounts: map[int64]*Account{1: account},
	}}
	upstream := &httpUpstreamRecorder{resp: poolInfoResponse(http.StatusOK, `{"mode":"unrestricted","balance":42.5,"remaining":42.5,"unit":"USD"}`)}
	svc := newUpstreamBillingProbeTestService(repo, upstream, &upstreamBillingProbeSettingRepo{})

	snapshot, err := svc.ProbePoolUpstreamInfo(context.Background(), 1)
	require.NoError(t, err)
	require.NotNil(t, snapshot)
	require.Equal(t, PoolUpstreamInfoStatusOK, snapshot.Status)
	require.Equal(t, PoolUpstreamPlatformSub2API, snapshot.Platform)
	require.Equal(t, "wallet", snapshot.Data["kind"])
	require.Equal(t, 42.5, snapshot.Data["amount_usd"])
	require.Equal(t, http.StatusOK, snapshot.HTTPStatus)
	require.NotNil(t, snapshot.FreshUntil)
	require.True(t, snapshot.NextProbeAt.After(snapshot.LastAttemptAt))

	require.NotNil(t, upstream.lastReq)
	require.Equal(t, "https://pool.example/v1/usage", upstream.lastReq.URL.String())
	require.Equal(t, http.MethodGet, upstream.lastReq.Method)
	require.Equal(t, "Bearer sk-pool-info", upstream.lastReq.Header.Get("Authorization"))
	require.True(t, HTTPUpstreamRedirectsDisabled(upstream.lastReq.Context()))

	stored, ok := repo.accounts[1].Extra[PoolUpstreamInfoExtraKey].(*PoolUpstreamInfoSnapshot)
	require.True(t, ok)
	require.Equal(t, PoolUpstreamInfoStatusOK, stored.Status)
}

func TestProbePoolUpstreamInfoChatGPT2APIEndpoint(t *testing.T) {
	account := newPoolUpstreamInfoAccount(1, PlatformOpenAI, PoolUpstreamPlatformChatGPT2API,
		[]string{PoolUpstreamFeatureAccountCount, PoolUpstreamFeatureImageQuota})
	account.Credentials["base_url"] = "https://pool.example/api/v1"
	repo := &poolUpstreamInfoRepo{upstreamBillingProbeAccountRepo: &upstreamBillingProbeAccountRepo{
		accounts: map[int64]*Account{1: account},
	}}
	upstream := &httpUpstreamRecorder{resp: poolInfoResponse(http.StatusOK,
		`{"accounts":{"total":1,"active":1,"total_quota":25,"unlimited_quota_count":0,"unknown_quota_count":0}}`)}
	svc := newUpstreamBillingProbeTestService(repo, upstream, &upstreamBillingProbeSettingRepo{})

	snapshot, err := svc.ProbePoolUpstreamInfo(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, PoolUpstreamInfoStatusOK, snapshot.Status)
	require.Equal(t, int64(1), snapshot.Data["accounts_active"])
	require.Equal(t, int64(25), snapshot.Data["total_quota"])
	require.Equal(t, "https://pool.example/api/dashboard", upstream.lastReq.URL.String())
}

func TestProbePoolUpstreamInfoFailureStatuses(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		body       string
		wantStatus string
		wantError  string
		wantHTTP   int
	}{
		{"unauthorized", http.StatusUnauthorized, `{"error":"no"}`, PoolUpstreamInfoStatusFailed, "unauthorized", 401},
		{"forbidden", http.StatusForbidden, `{}`, PoolUpstreamInfoStatusFailed, "forbidden", 403},
		{"not found is unsupported", http.StatusNotFound, `{}`, PoolUpstreamInfoStatusUnsupported, "unsupported", 404},
		{"method not allowed is unsupported", http.StatusMethodNotAllowed, `{}`, PoolUpstreamInfoStatusUnsupported, "unsupported", 405},
		{"malformed body", http.StatusOK, `not json`, PoolUpstreamInfoStatusFailed, "invalid_response", 200},
		{"missing required field", http.StatusOK, `{"mode":"quota_limited","unit":"USD"}`, PoolUpstreamInfoStatusFailed, "invalid_response", 200},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			account := newPoolUpstreamInfoAccount(1, PlatformAnthropic, PoolUpstreamPlatformSub2API, []string{PoolUpstreamFeatureBalance})
			repo := &poolUpstreamInfoRepo{upstreamBillingProbeAccountRepo: &upstreamBillingProbeAccountRepo{
				accounts: map[int64]*Account{1: account},
			}}
			upstream := &httpUpstreamRecorder{resp: poolInfoResponse(tc.status, tc.body)}
			svc := newUpstreamBillingProbeTestService(repo, upstream, &upstreamBillingProbeSettingRepo{})

			snapshot, err := svc.ProbePoolUpstreamInfo(context.Background(), 1)
			require.NoError(t, err)
			require.Equal(t, tc.wantStatus, snapshot.Status)
			require.Equal(t, tc.wantError, snapshot.LastError)
			require.Equal(t, tc.wantHTTP, snapshot.HTTPStatus)
			require.Equal(t, 1, snapshot.FailureCount)
			require.True(t, snapshot.NextProbeAt.After(snapshot.LastAttemptAt))
		})
	}
}

func TestProbePoolUpstreamInfoOversizedResponse(t *testing.T) {
	account := newPoolUpstreamInfoAccount(1, PlatformAnthropic, PoolUpstreamPlatformSub2API, []string{PoolUpstreamFeatureBalance})
	repo := &poolUpstreamInfoRepo{upstreamBillingProbeAccountRepo: &upstreamBillingProbeAccountRepo{
		accounts: map[int64]*Account{1: account},
	}}
	upstream := &httpUpstreamRecorder{resp: poolInfoResponse(http.StatusOK, strings.Repeat("x", upstreamBillingProbeMaxBodyBytes+10))}
	svc := newUpstreamBillingProbeTestService(repo, upstream, &upstreamBillingProbeSettingRepo{})

	snapshot, err := svc.ProbePoolUpstreamInfo(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, PoolUpstreamInfoStatusFailed, snapshot.Status)
	require.Equal(t, "response_too_large", snapshot.LastError)
}

// The dashboard endpoint returns charts alongside the small accounts object,
// so chatgpt2api probes get a 1 MiB cap while sub2api keeps 64 KiB.
func TestProbePoolUpstreamInfoChatGPT2APIDashboardSizeLimits(t *testing.T) {
	largeDashboard := `{"accounts":{"total":1,"active":1,"total_quota":25,"unlimited_quota_count":0,"unknown_quota_count":0},"charts":{"pad":"` +
		strings.Repeat("x", 70*1024) + `"}}`
	t.Run("70KiB well-formed dashboard succeeds", func(t *testing.T) {
		account := newPoolUpstreamInfoAccount(1, PlatformOpenAI, PoolUpstreamPlatformChatGPT2API,
			[]string{PoolUpstreamFeatureAccountCount, PoolUpstreamFeatureImageQuota})
		repo := &poolUpstreamInfoRepo{upstreamBillingProbeAccountRepo: &upstreamBillingProbeAccountRepo{
			accounts: map[int64]*Account{1: account},
		}}
		upstream := &httpUpstreamRecorder{resp: poolInfoResponse(http.StatusOK, largeDashboard)}
		svc := newUpstreamBillingProbeTestService(repo, upstream, &upstreamBillingProbeSettingRepo{})

		snapshot, err := svc.ProbePoolUpstreamInfo(context.Background(), 1)
		require.NoError(t, err)
		require.Equal(t, PoolUpstreamInfoStatusOK, snapshot.Status)
		require.Equal(t, int64(25), snapshot.Data["total_quota"])
	})

	t.Run("dashboard over 1MiB fails", func(t *testing.T) {
		account := newPoolUpstreamInfoAccount(1, PlatformOpenAI, PoolUpstreamPlatformChatGPT2API,
			[]string{PoolUpstreamFeatureAccountCount, PoolUpstreamFeatureImageQuota})
		repo := &poolUpstreamInfoRepo{upstreamBillingProbeAccountRepo: &upstreamBillingProbeAccountRepo{
			accounts: map[int64]*Account{1: account},
		}}
		upstream := &httpUpstreamRecorder{resp: poolInfoResponse(http.StatusOK,
			`{"accounts":`+strings.Repeat("x", 1024*1024+8))}
		svc := newUpstreamBillingProbeTestService(repo, upstream, &upstreamBillingProbeSettingRepo{})

		snapshot, err := svc.ProbePoolUpstreamInfo(context.Background(), 1)
		require.NoError(t, err)
		require.Equal(t, PoolUpstreamInfoStatusFailed, snapshot.Status)
		require.Equal(t, "response_too_large", snapshot.LastError)
	})
}

func TestProbePoolUpstreamInfoRedirectAndTimeout(t *testing.T) {
	t.Run("redirect is a safe failure and is never followed", func(t *testing.T) {
		account := newPoolUpstreamInfoAccount(1, PlatformAnthropic, PoolUpstreamPlatformSub2API, []string{PoolUpstreamFeatureBalance})
		repo := &poolUpstreamInfoRepo{upstreamBillingProbeAccountRepo: &upstreamBillingProbeAccountRepo{
			accounts: map[int64]*Account{1: account},
		}}
		upstream := &httpUpstreamRecorder{resp: &http.Response{
			StatusCode: http.StatusFound,
			Header:     http.Header{"Location": []string{"https://attacker.example/steal"}},
			Body:       io.NopCloser(strings.NewReader("")),
		}}
		svc := newUpstreamBillingProbeTestService(repo, upstream, &upstreamBillingProbeSettingRepo{})

		snapshot, err := svc.ProbePoolUpstreamInfo(context.Background(), 1)
		require.NoError(t, err)
		require.Equal(t, PoolUpstreamInfoStatusFailed, snapshot.Status)
		require.Equal(t, "http_error", snapshot.LastError)
		require.Equal(t, http.StatusFound, snapshot.HTTPStatus)
		require.Len(t, upstream.requests, 1)
		require.True(t, HTTPUpstreamRedirectsDisabled(upstream.lastReq.Context()))
		require.Nil(t, snapshot.Data)
	})

	t.Run("timeout records a safe failure reason", func(t *testing.T) {
		account := newPoolUpstreamInfoAccount(1, PlatformAnthropic, PoolUpstreamPlatformSub2API, []string{PoolUpstreamFeatureBalance})
		repo := &poolUpstreamInfoRepo{upstreamBillingProbeAccountRepo: &upstreamBillingProbeAccountRepo{
			accounts: map[int64]*Account{1: account},
		}}
		upstream := &httpUpstreamRecorder{err: context.DeadlineExceeded}
		svc := newUpstreamBillingProbeTestService(repo, upstream, &upstreamBillingProbeSettingRepo{})

		snapshot, err := svc.ProbePoolUpstreamInfo(context.Background(), 1)
		require.NoError(t, err)
		require.Equal(t, PoolUpstreamInfoStatusFailed, snapshot.Status)
		require.Equal(t, "request_failed", snapshot.LastError)
		require.Equal(t, 0, snapshot.HTTPStatus)
	})
}

// The info probe has no official-domain fallback: an explicitly selected
// chatgpt2api/sub2api endpoint can only live on a self-hosted relay, so empty
// or official-provider base URLs must never trigger a network call that would
// carry the account key to an unrelated official route.
func TestProbePoolUpstreamInfoNeverFallsBackToOfficialAPI(t *testing.T) {
	cases := []struct {
		name       string
		baseURL    string
		wantStatus string
		wantError  string
	}{
		{"empty base url", "", PoolUpstreamInfoStatusFailed, "invalid_base_url"},
		{"official openai domain", "https://api.openai.com", PoolUpstreamInfoStatusUnsupported, "unsupported"},
		{"official openai v1 base", "https://api.openai.com/v1", PoolUpstreamInfoStatusUnsupported, "unsupported"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			account := newPoolUpstreamInfoAccount(1, PlatformOpenAI, PoolUpstreamPlatformChatGPT2API,
				[]string{PoolUpstreamFeatureAccountCount, PoolUpstreamFeatureImageQuota})
			account.Credentials["base_url"] = tc.baseURL
			repo := &poolUpstreamInfoRepo{upstreamBillingProbeAccountRepo: &upstreamBillingProbeAccountRepo{
				accounts: map[int64]*Account{1: account},
			}}
			upstream := &httpUpstreamRecorder{}
			svc := newUpstreamBillingProbeTestService(repo, upstream, &upstreamBillingProbeSettingRepo{})

			snapshot, err := svc.ProbePoolUpstreamInfo(context.Background(), 1)
			require.NoError(t, err)
			require.Equal(t, tc.wantStatus, snapshot.Status)
			require.Equal(t, tc.wantError, snapshot.LastError)
			require.Equal(t, 0, snapshot.HTTPStatus)
			require.Empty(t, upstream.requests)
		})
	}

	// Positive control: a custom relay base URL still probes normally.
	account := newPoolUpstreamInfoAccount(2, PlatformOpenAI, PoolUpstreamPlatformChatGPT2API,
		[]string{PoolUpstreamFeatureAccountCount})
	repo := &poolUpstreamInfoRepo{upstreamBillingProbeAccountRepo: &upstreamBillingProbeAccountRepo{
		accounts: map[int64]*Account{2: account},
	}}
	upstream := &httpUpstreamRecorder{resp: poolInfoResponse(http.StatusOK,
		`{"accounts":{"total":1,"active":2}}`)}
	svc := newUpstreamBillingProbeTestService(repo, upstream, &upstreamBillingProbeSettingRepo{})

	snapshot, err := svc.ProbePoolUpstreamInfo(context.Background(), 2)
	require.NoError(t, err)
	require.Equal(t, PoolUpstreamInfoStatusOK, snapshot.Status)
	require.Equal(t, int64(2), snapshot.Data["accounts_active"])
	require.Len(t, upstream.requests, 1)
	require.Equal(t, "https://pool.example/api/dashboard", upstream.lastReq.URL.String())
}

func TestProbePoolUpstreamInfoPreservesLastSuccessOnFailure(t *testing.T) {
	receivedAt := time.Now().Add(-40 * time.Minute).UTC()
	freshUntil := receivedAt.Add(time.Hour)
	previous := &PoolUpstreamInfoSnapshot{
		Status:     PoolUpstreamInfoStatusOK,
		Platform:   PoolUpstreamPlatformSub2API,
		Features:   []string{PoolUpstreamFeatureBalance},
		Data:       map[string]any{"kind": "wallet", "amount_usd": 12.5},
		ReceivedAt: &receivedAt,
		FreshUntil: &freshUntil,
	}
	account := newPoolUpstreamInfoAccount(1, PlatformAnthropic, PoolUpstreamPlatformSub2API, []string{PoolUpstreamFeatureBalance})
	account.Extra[PoolUpstreamInfoExtraKey] = previous
	repo := &poolUpstreamInfoRepo{upstreamBillingProbeAccountRepo: &upstreamBillingProbeAccountRepo{
		accounts: map[int64]*Account{1: account},
	}}
	upstream := &httpUpstreamRecorder{resp: poolInfoResponse(http.StatusInternalServerError, `{}`)}
	svc := newUpstreamBillingProbeTestService(repo, upstream, &upstreamBillingProbeSettingRepo{})

	snapshot, err := svc.ProbePoolUpstreamInfo(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, PoolUpstreamInfoStatusFailed, snapshot.Status)
	require.Equal(t, "http_error", snapshot.LastError)
	require.Equal(t, 1, snapshot.FailureCount)
	// Last successful data and freshness are preserved, marking it stale.
	require.Equal(t, "wallet", snapshot.Data["kind"])
	require.Equal(t, 12.5, snapshot.Data["amount_usd"])
	require.Equal(t, &receivedAt, snapshot.ReceivedAt)
	require.Equal(t, &freshUntil, snapshot.FreshUntil)
}

func TestProbePoolUpstreamInfoCASConflict(t *testing.T) {
	account := newPoolUpstreamInfoAccount(1, PlatformAnthropic, PoolUpstreamPlatformSub2API, []string{PoolUpstreamFeatureBalance})
	repo := &poolUpstreamInfoRepo{
		upstreamBillingProbeAccountRepo: &upstreamBillingProbeAccountRepo{
			accounts: map[int64]*Account{1: account},
		},
	}
	// Simulate an admin rotating the API key while the probe is in flight.
	repo.mutateBeforeWrite = func() {
		repo.accounts[1].Credentials["api_key"] = "sk-rotated"
	}
	upstream := &httpUpstreamRecorder{resp: poolInfoResponse(http.StatusOK, `{"mode":"unrestricted","balance":1,"unit":"USD"}`)}
	svc := newUpstreamBillingProbeTestService(repo, upstream, &upstreamBillingProbeSettingRepo{})

	_, err := svc.ProbePoolUpstreamInfo(context.Background(), 1)
	require.ErrorIs(t, err, ErrPoolUpstreamInfoIdentityChanged)
	_, present := repo.accounts[1].Extra[PoolUpstreamInfoExtraKey]
	require.False(t, present)
}

func TestProbePoolUpstreamInfoRejectsUnconfiguredAccounts(t *testing.T) {
	repo := &poolUpstreamInfoRepo{upstreamBillingProbeAccountRepo: &upstreamBillingProbeAccountRepo{
		accounts: map[int64]*Account{
			1: newPoolUpstreamInfoAccount(1, PlatformAnthropic, "", nil),
			2: func() *Account {
				account := newPoolUpstreamInfoAccount(2, PlatformAnthropic, PoolUpstreamPlatformSub2API, []string{PoolUpstreamFeatureBalance})
				account.Credentials["pool_mode"] = false
				return account
			}(),
			3: func() *Account {
				account := newPoolUpstreamInfoAccount(3, PlatformAnthropic, PoolUpstreamPlatformSub2API, []string{PoolUpstreamFeatureBalance})
				account.Type = AccountTypeOAuth
				return account
			}(),
			4: newPoolUpstreamInfoAccount(4, PlatformAnthropic, PoolUpstreamPlatformChatGPT2API,
				[]string{PoolUpstreamFeatureAccountCount}),
		},
	}}
	upstream := &httpUpstreamRecorder{}
	svc := newUpstreamBillingProbeTestService(repo, upstream, &upstreamBillingProbeSettingRepo{})

	_, err := svc.ProbePoolUpstreamInfo(context.Background(), 1)
	require.ErrorIs(t, err, ErrPoolUpstreamInfoNotConfigured)
	_, err = svc.ProbePoolUpstreamInfo(context.Background(), 2)
	require.ErrorIs(t, err, ErrPoolUpstreamInfoAccountInvalid)
	_, err = svc.ProbePoolUpstreamInfo(context.Background(), 3)
	require.ErrorIs(t, err, ErrPoolUpstreamInfoAccountInvalid)
	// A chatgpt2api selection on a non-OpenAI account is rejected too.
	_, err = svc.ProbePoolUpstreamInfo(context.Background(), 4)
	require.ErrorIs(t, err, ErrPoolUpstreamInfoAccountInvalid)
	require.Empty(t, upstream.requests)
}

// A row that lost pool-mode eligibility between the due query and the probe
// must be skipped without any upstream request.
func TestRunDuePoolUpstreamInfoSkipsIneligibleRows(t *testing.T) {
	account := newPoolUpstreamInfoAccount(1, PlatformAnthropic, PoolUpstreamPlatformSub2API, []string{PoolUpstreamFeatureBalance})
	account.Credentials["pool_mode"] = false
	repo := &poolUpstreamInfoRepo{
		upstreamBillingProbeAccountRepo: &upstreamBillingProbeAccountRepo{
			accounts: map[int64]*Account{1: account},
		},
		due: []Account{*account},
	}
	upstream := &httpUpstreamRecorder{}
	svc := newUpstreamBillingProbeTestService(repo, upstream, &upstreamBillingProbeSettingRepo{})

	require.NoError(t, svc.RunDue(context.Background()))
	require.Empty(t, upstream.requests)
	_, present := repo.accounts[1].Extra[PoolUpstreamInfoExtraKey]
	require.False(t, present)
}

func TestRunDuePoolUpstreamInfoRunsWhenBillingDisabled(t *testing.T) {
	account := newPoolUpstreamInfoAccount(1, PlatformAnthropic, PoolUpstreamPlatformSub2API, []string{PoolUpstreamFeatureBalance})
	repo := &poolUpstreamInfoRepo{
		upstreamBillingProbeAccountRepo: &upstreamBillingProbeAccountRepo{
			accounts: map[int64]*Account{1: account},
		},
		due: []Account{*account},
	}
	settings := &upstreamBillingProbeSettingRepo{values: map[string]string{
		SettingKeyUpstreamBillingProbeSettings: `{"enabled":false,"interval_minutes":30}`,
	}}
	upstream := &httpUpstreamRecorder{resp: poolInfoResponse(http.StatusOK, `{"mode":"quota_limited","remaining":7,"unit":"USD"}`)}
	svc := newUpstreamBillingProbeTestService(repo, upstream, settings)

	require.NoError(t, svc.RunDue(context.Background()))
	require.Len(t, upstream.requests, 1)
	require.Equal(t, "quota", repo.accounts[1].Extra[PoolUpstreamInfoExtraKey].(*PoolUpstreamInfoSnapshot).Data["kind"])
}

func TestRunDuePoolUpstreamInfoSkipsFreshSnapshots(t *testing.T) {
	next := time.Now().Add(time.Hour)
	previous := &PoolUpstreamInfoSnapshot{
		Status:      PoolUpstreamInfoStatusOK,
		Platform:    PoolUpstreamPlatformSub2API,
		Features:    []string{PoolUpstreamFeatureBalance},
		Data:        map[string]any{"kind": "wallet", "amount_usd": 1},
		NextProbeAt: next,
	}
	account := newPoolUpstreamInfoAccount(1, PlatformAnthropic, PoolUpstreamPlatformSub2API, []string{PoolUpstreamFeatureBalance})
	account.Extra[PoolUpstreamInfoExtraKey] = previous
	repo := &poolUpstreamInfoRepo{
		upstreamBillingProbeAccountRepo: &upstreamBillingProbeAccountRepo{
			accounts: map[int64]*Account{1: account},
		},
		due: []Account{*account},
	}
	upstream := &httpUpstreamRecorder{}
	svc := newUpstreamBillingProbeTestService(repo, upstream, &upstreamBillingProbeSettingRepo{})

	require.NoError(t, svc.RunDue(context.Background()))
	require.Empty(t, upstream.requests)
}

func TestProbePoolUpstreamInfoNeverUsesClientSuppliedSnapshot(t *testing.T) {
	// A forged pool_upstream_info in extra cannot survive account reads: the
	// tolerant decoder keeps it, but probe write protection only accepts the
	// server-managed type. Verify applyPoolUpstreamSelectionForCreate drops it.
	account := newPoolUpstreamInfoAccount(1, PlatformAnthropic, PoolUpstreamPlatformSub2API, []string{PoolUpstreamFeatureBalance})
	account.Extra[PoolUpstreamInfoExtraKey] = map[string]any{"status": "ok", "data": map[string]any{"amount_usd": 99999}}
	require.NoError(t, applyPoolUpstreamSelectionForCreate(account))
	_, present := account.Extra[PoolUpstreamInfoExtraKey]
	require.False(t, present)
}

func TestSafePoolUpstreamInfoError(t *testing.T) {
	require.Equal(t, "", safePoolUpstreamInfoError(nil))
	require.Equal(t, ErrPoolUpstreamInfoNotConfigured.Error(), safePoolUpstreamInfoError(ErrPoolUpstreamInfoNotConfigured))
	require.Equal(t, "probe_failed", safePoolUpstreamInfoError(errors.New("boom")))
}
