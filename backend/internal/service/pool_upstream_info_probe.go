package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"golang.org/x/sync/errgroup"
)

// The pool upstream information probe shares the billing probe's process-wide
// runner, transport, and bounded execution infrastructure but keeps an
// independent leader key, due queue, failure state, and snapshot so billing
// enablement or billing failures never delay it.
const poolUpstreamInfoLeaderLockKey = "upstream:pool_info:probe:leader"

func poolUpstreamInfoLeaderLockKeyAt(now time.Time) string {
	return fmt.Sprintf("%s:%d", poolUpstreamInfoLeaderLockKey, now.Unix()/int64(upstreamBillingProbeCycleInterval/time.Second))
}

type poolUpstreamInfoSnapshotWriter interface {
	UpdatePoolUpstreamInfoSnapshot(context.Context, *Account, *PoolUpstreamInfoSnapshot) error
}

type poolUpstreamInfoDueAccountLister interface {
	ListDuePoolUpstreamInfoAccounts(context.Context, time.Time, int) ([]Account, error)
}

// runPoolUpstreamInfoDueLocked executes at most one bounded batch of due
// information probes. Callers must hold s.cycleMu. Unlike the billing run this
// path is not gated on the billing probe global switch.
func (s *UpstreamBillingProbeService) runPoolUpstreamInfoDueLocked(ctx context.Context) error {
	runRelease, acquired, lockErr := s.tryAcquireLeaderLock(ctx, poolUpstreamInfoLeaderLockKey)
	if lockErr != nil {
		return fmt.Errorf("acquire pool upstream info leader lock: %w", lockErr)
	}
	if !acquired {
		return nil
	}
	defer runRelease()

	lockNow := time.Now()
	cadenceRelease, acquired, lockErr := s.tryAcquireLeaderLock(ctx, poolUpstreamInfoLeaderLockKeyAt(lockNow))
	if lockErr != nil {
		return fmt.Errorf("acquire pool upstream info cadence lock: %w", lockErr)
	}
	if !acquired {
		return nil
	}
	defer releaseUpstreamBillingProbeLeaderLock(cadenceRelease, lockNow.Truncate(upstreamBillingProbeCycleInterval).Add(upstreamBillingProbeCycleInterval))

	now := s.currentTime()
	accounts, err := s.listDuePoolUpstreamInfoAccounts(ctx, now)
	if err != nil {
		return fmt.Errorf("list due pool upstream info accounts: %w", err)
	}
	due := make([]Account, 0, len(accounts))
	for i := range accounts {
		account := accounts[i]
		if !account.IsActive() || !isPoolUpstreamInfoProbeAccount(&account) {
			continue
		}
		snapshot := decodePoolUpstreamInfoSnapshot(account.Extra)
		if snapshot != nil && !snapshot.NextProbeAt.IsZero() && now.Before(snapshot.NextProbeAt) {
			continue
		}
		due = append(due, account)
	}
	sort.SliceStable(due, func(i, j int) bool {
		left := decodePoolUpstreamInfoSnapshot(due[i].Extra)
		right := decodePoolUpstreamInfoSnapshot(due[j].Extra)
		leftUnset := left == nil || left.NextProbeAt.IsZero()
		rightUnset := right == nil || right.NextProbeAt.IsZero()
		if leftUnset && rightUnset {
			return due[i].ID < due[j].ID
		}
		if leftUnset {
			return true
		}
		if rightUnset {
			return false
		}
		return left.NextProbeAt.Before(right.NextProbeAt)
	})
	if len(due) > upstreamBillingProbeMaxPerCycle {
		due = due[:upstreamBillingProbeMaxPerCycle]
	}

	var group errgroup.Group
	for i := range due {
		accountID := due[i].ID
		group.Go(func() error {
			if _, probeErr := s.probePoolUpstreamInfoAccount(ctx, accountID, true); probeErr != nil {
				logger.LegacyPrintf("service.pool_upstream_info", "probe_due_failed: account_id=%d err=%v", accountID, probeErr)
			}
			return nil
		})
	}
	return group.Wait()
}

func (s *UpstreamBillingProbeService) listDuePoolUpstreamInfoAccounts(ctx context.Context, now time.Time) ([]Account, error) {
	if lister, ok := s.accountRepo.(poolUpstreamInfoDueAccountLister); ok {
		return lister.ListDuePoolUpstreamInfoAccounts(ctx, now, upstreamBillingProbeMaxPerCycle)
	}
	// Non-production repositories and older adapters keep the generic path. The
	// runner still re-filters eligibility and truncates before network calls.
	seen := make(map[int64]struct{})
	out := make([]Account, 0)
	for _, platform := range []string{PoolUpstreamPlatformSub2API, PoolUpstreamPlatformChatGPT2API} {
		accounts, err := s.accountRepo.FindByExtraField(ctx, PoolUpstreamPlatformExtraKey, platform)
		if err != nil {
			return nil, err
		}
		for _, account := range accounts {
			if _, exists := seen[account.ID]; exists {
				continue
			}
			seen[account.ID] = struct{}{}
			out = append(out, account)
		}
	}
	return out, nil
}

// ProbePoolUpstreamInfo performs one manual information probe for an enabled
// account. Manual calls ignore scheduling state but still require an eligible
// account with a configured selection.
func (s *UpstreamBillingProbeService) ProbePoolUpstreamInfo(ctx context.Context, accountID int64) (*PoolUpstreamInfoSnapshot, error) {
	if s == nil || s.accountRepo == nil {
		return nil, ErrPoolUpstreamInfoUnavailable
	}
	return s.probePoolUpstreamInfoAccount(ctx, accountID, false)
}

func (s *UpstreamBillingProbeService) probePoolUpstreamInfoAccount(ctx context.Context, accountID int64, requireConfigured bool) (*PoolUpstreamInfoSnapshot, error) {
	key := "pool_info:" + strconv.FormatInt(accountID, 10)
	value, err, _ := s.probeGroup.Do(key, func() (any, error) {
		select {
		case s.probeSlots <- struct{}{}:
			defer func() { <-s.probeSlots }()
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		account, loadErr := s.accountRepo.GetByID(ctx, accountID)
		if loadErr != nil {
			return nil, loadErr
		}
		if !isPoolUpstreamInfoEligible(account) {
			return nil, ErrPoolUpstreamInfoAccountInvalid
		}
		platform, features := PoolUpstreamSelection(account.Extra)
		if platform == PoolUpstreamPlatformDefault || len(features) == 0 {
			if requireConfigured {
				// Scheduled path: account dropped out of the due set between the
				// list query and the load, e.g. an admin disabled the feature.
				return nil, nil
			}
			return nil, ErrPoolUpstreamInfoNotConfigured
		}
		if platform == PoolUpstreamPlatformChatGPT2API && account.Platform != PlatformOpenAI {
			// chatgpt2api is restricted to OpenAI API-key accounts. A stored
			// selection can only mismatch the provider platform if written
			// before the validation existed; treat it as ineligible.
			if requireConfigured {
				return nil, nil
			}
			return nil, ErrPoolUpstreamInfoAccountInvalid
		}
		if requireConfigured {
			if !account.IsActive() {
				return nil, nil
			}
			if snapshot := decodePoolUpstreamInfoSnapshot(account.Extra); snapshot != nil &&
				!snapshot.NextProbeAt.IsZero() && s.currentTime().Before(snapshot.NextProbeAt) {
				return nil, nil
			}
		}
		return s.probeLoadedPoolUpstreamInfoAccount(ctx, account)
	})
	if err != nil {
		return nil, err
	}
	if value == nil {
		return nil, nil
	}
	snapshot, ok := value.(*PoolUpstreamInfoSnapshot)
	if !ok {
		return nil, fmt.Errorf("invalid pool upstream info probe result")
	}
	return snapshot, nil
}

func (s *UpstreamBillingProbeService) probeLoadedPoolUpstreamInfoAccount(ctx context.Context, account *Account) (*PoolUpstreamInfoSnapshot, error) {
	now := s.currentTime().UTC()
	fail := func(statusCode int, reason string, retryAfterDuration time.Duration) (*PoolUpstreamInfoSnapshot, error) {
		return s.persistPoolUpstreamInfoFailure(ctx, account, now, statusCode, reason, retryAfterDuration)
	}
	if s.accountTestService == nil || s.accountTestService.httpUpstream == nil {
		return fail(0, "transport_unavailable", 0)
	}
	platform, features := PoolUpstreamSelection(account.Extra)
	if platform == PoolUpstreamPlatformDefault || len(features) == 0 {
		return nil, nil
	}
	apiKey := account.GetCredential("api_key")
	if apiKey == "" {
		return fail(0, "missing_api_key", 0)
	}
	baseURL := account.GetCredential("base_url")
	if strings.TrimSpace(baseURL) == "" {
		// 信息探测没有官方域兜底：显式选择的上游信息接口只能存在于自建
		// relay，空 base_url 直接记 invalid_base_url，绝不把（可能是
		// chatgpt2api 管理员级别的）Key 发往无关官方路由。
		return fail(0, "invalid_base_url", 0)
	}
	if upstreamBillingProbeTargetIsOfficialAPI(baseURL) {
		// 官方 API 域不可能提供 /v1/usage 或 /api/dashboard；不发请求，直接
		// 记 unsupported，避免周期性把账号 Key 发往官方域的不存在路径。
		return fail(0, "unsupported", 0)
	}
	normalizedBaseURL, err := s.accountTestService.validateUpstreamBaseURL(baseURL)
	if err != nil {
		return fail(0, "invalid_base_url", 0)
	}
	probeURL, err := poolUpstreamInfoEndpointURL(normalizedBaseURL, platform)
	if err != nil {
		return fail(0, "invalid_base_url", 0)
	}
	proxyURL := ""
	if account.ProxyID != nil {
		if account.Proxy == nil {
			return fail(0, "proxy_unavailable", 0)
		}
		if account.Proxy.ID != *account.ProxyID {
			return nil, ErrPoolUpstreamInfoIdentityChanged
		}
		proxyURL = account.Proxy.URL()
	}
	probeCtx, cancel := context.WithTimeout(ctx, upstreamBillingProbeRequestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, probeURL, bytes.NewReader(nil))
	if err != nil {
		return fail(0, "request_build_failed", 0)
	}
	// OpenAI 账号保持官方 openai 传输画像；其他平台走默认画像（与 billing probe 一致）。
	profile := HTTPUpstreamProfileDefault
	if account.Platform == PlatformOpenAI {
		profile = HTTPUpstreamProfileOpenAI
	}
	reqCtx := WithHTTPUpstreamProfile(req.Context(), profile)
	req = req.WithContext(WithHTTPUpstreamRedirectsDisabled(reqCtx))
	req.Header.Set("Accept", "application/json")
	// 先应用可配置的 header 覆写，再固定写入 Authorization：覆写集合不允许
	// authorization/x-api-key 等敏感头（见 header_override.go 的封禁列表），这里再
	// 后置写入兜底，确保任何配置都无法篡改或泄露探测凭证。
	account.ApplyHeaderOverrides(req.Header)
	req.Header.Set("Authorization", "Bearer "+apiKey)
	var tlsProfile *tlsfingerprint.Profile
	if s.accountTestService.tlsFPProfileService != nil {
		tlsProfile = s.accountTestService.tlsFPProfileService.ResolveTLSProfile(account)
	}
	resp, err := s.accountTestService.httpUpstream.DoWithTLS(req, proxyURL, account.ID, account.Concurrency, tlsProfile)
	if err != nil {
		return fail(0, "request_failed", 0)
	}
	if resp == nil || resp.Body == nil {
		return fail(0, "empty_response", 0)
	}
	defer func() { _ = resp.Body.Close() }()
	// The ChatGPT2API dashboard carries full charts alongside the small
	// accounts object, so it gets a larger cap than the compact usage payload.
	bodyLimit := int64(upstreamBillingProbeMaxBodyBytes)
	if platform == PoolUpstreamPlatformChatGPT2API {
		bodyLimit = 1024 * 1024
	}
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, bodyLimit+1))
	if readErr != nil {
		return fail(resp.StatusCode, "response_read_failed", retryAfter(resp.Header, now))
	}
	if int64(len(body)) > bodyLimit {
		return fail(resp.StatusCode, "response_too_large", retryAfter(resp.Header, now))
	}
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed {
		return fail(resp.StatusCode, "unsupported", retryAfter(resp.Header, now))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		reason := "http_error"
		switch resp.StatusCode {
		case http.StatusUnauthorized:
			reason = "unauthorized"
		case http.StatusForbidden:
			reason = "forbidden"
		}
		return fail(resp.StatusCode, reason, retryAfter(resp.Header, now))
	}
	var data map[string]any
	switch platform {
	case PoolUpstreamPlatformSub2API:
		data, err = parsePoolUpstreamSub2APIUsage(body)
	case PoolUpstreamPlatformChatGPT2API:
		data, err = parsePoolUpstreamChatGPT2APIDashboard(body, features)
	default:
		err = fmt.Errorf("unsupported pool upstream platform")
	}
	if err != nil {
		return fail(resp.StatusCode, "invalid_response", retryAfter(resp.Header, now))
	}
	snapshot := &PoolUpstreamInfoSnapshot{
		Status:        PoolUpstreamInfoStatusOK,
		Platform:      platform,
		Features:      features,
		Data:          data,
		ReceivedAt:    probeTimePtr(now),
		FreshUntil:    probeTimePtr(now.Add(2 * time.Duration(poolUpstreamInfoIntervalMinutes) * time.Minute)),
		LastAttemptAt: now,
		NextProbeAt:   now.Add(nextProbeDelay(poolUpstreamInfoIntervalMinutes, 0)),
		HTTPStatus:    resp.StatusCode,
	}
	if err := s.updatePoolUpstreamInfoSnapshot(ctx, account, snapshot); err != nil {
		return nil, err
	}
	return snapshot, nil
}

func (s *UpstreamBillingProbeService) persistPoolUpstreamInfoFailure(
	ctx context.Context,
	account *Account,
	now time.Time,
	statusCode int,
	reason string,
	retryAfterDuration time.Duration,
) (*PoolUpstreamInfoSnapshot, error) {
	previous := decodePoolUpstreamInfoSnapshot(account.Extra)
	failureCount := 1
	if previous != nil {
		failureCount = previous.FailureCount + 1
	}
	status := PoolUpstreamInfoStatusFailed
	delay := nextProbeDelay(poolUpstreamInfoIntervalMinutes, retryAfterDuration)
	if reason == "unsupported" {
		status = PoolUpstreamInfoStatusUnsupported
		delay = unsupportedProbeDelay(poolUpstreamInfoIntervalMinutes, retryAfterDuration)
	}
	platform, features := PoolUpstreamSelection(account.Extra)
	snapshot := &PoolUpstreamInfoSnapshot{
		Status:        status,
		Platform:      platform,
		Features:      features,
		LastAttemptAt: now,
		NextProbeAt:   now.Add(delay),
		FailureCount:  failureCount,
		HTTPStatus:    statusCode,
		LastError:     reason,
	}
	if previous != nil {
		// A failed or unsupported attempt preserves the last successful values
		// and marks them stale via the unchanged fresh_until.
		snapshot.Data = previous.Data
		snapshot.ReceivedAt = previous.ReceivedAt
		snapshot.FreshUntil = previous.FreshUntil
		if snapshot.FreshUntil == nil && previous.Status == PoolUpstreamInfoStatusOK && previous.ReceivedAt != nil {
			snapshot.FreshUntil = probeTimePtr(previous.ReceivedAt.Add(2 * time.Duration(poolUpstreamInfoIntervalMinutes) * time.Minute))
		}
	}
	if err := s.updatePoolUpstreamInfoSnapshot(ctx, account, snapshot); err != nil {
		return nil, err
	}
	return snapshot, nil
}

func (s *UpstreamBillingProbeService) updatePoolUpstreamInfoSnapshot(ctx context.Context, account *Account, snapshot *PoolUpstreamInfoSnapshot) error {
	writer, ok := s.accountRepo.(poolUpstreamInfoSnapshotWriter)
	if !ok {
		return ErrPoolUpstreamInfoUnavailable
	}
	return writer.UpdatePoolUpstreamInfoSnapshot(ctx, account, snapshot)
}

func decodePoolUpstreamInfoSnapshot(extra map[string]any) *PoolUpstreamInfoSnapshot {
	if extra == nil {
		return nil
	}
	value, ok := extra[PoolUpstreamInfoExtraKey]
	if !ok {
		return nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var snapshot PoolUpstreamInfoSnapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil || snapshot.Status == "" {
		return nil
	}
	switch snapshot.Status {
	case PoolUpstreamInfoStatusOK, PoolUpstreamInfoStatusUnsupported, PoolUpstreamInfoStatusFailed:
	default:
		return nil
	}
	return &snapshot
}

func safePoolUpstreamInfoError(err error) string {
	if err == nil {
		return ""
	}
	switch {
	case errors.Is(err, ErrPoolUpstreamInfoAccountInvalid):
		return ErrPoolUpstreamInfoAccountInvalid.Error()
	case errors.Is(err, ErrPoolUpstreamInfoNotConfigured):
		return ErrPoolUpstreamInfoNotConfigured.Error()
	case errors.Is(err, ErrPoolUpstreamInfoUnavailable):
		return ErrPoolUpstreamInfoUnavailable.Error()
	case errors.Is(err, ErrPoolUpstreamInfoIdentityChanged):
		return ErrPoolUpstreamInfoIdentityChanged.Error()
	}
	return "probe_failed"
}
