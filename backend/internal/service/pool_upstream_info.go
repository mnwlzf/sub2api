package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// Pool upstream information keys live in accounts.extra so no schema migration
// is required. The declared selection is admin-writable configuration while the
// snapshot is probe-managed runtime state.
const (
	PoolUpstreamPlatformExtraKey = "pool_upstream_platform"
	PoolUpstreamFeaturesExtraKey = "pool_upstream_features"
	PoolUpstreamInfoExtraKey     = "pool_upstream_info"
)

// Declared pool upstream platforms. "default" is the legacy no-probe state.
const (
	PoolUpstreamPlatformDefault     = "default"
	PoolUpstreamPlatformSub2API     = "sub2api"
	PoolUpstreamPlatformChatGPT2API = "chatgpt2api"
)

// Selectable information features per upstream platform.
const (
	PoolUpstreamFeatureBalance      = "balance"       // sub2api: wallet/quota balance via GET /v1/usage
	PoolUpstreamFeatureAccountCount = "account_count" // chatgpt2api: accounts.active via GET /api/dashboard
	PoolUpstreamFeatureImageQuota   = "image_quota"   // chatgpt2api: total_quota via GET /api/dashboard
)

// Snapshot statuses reuse the billing probe vocabulary.
const (
	PoolUpstreamInfoStatusOK          = UpstreamBillingProbeStatusOK
	PoolUpstreamInfoStatusUnsupported = UpstreamBillingProbeStatusUnsupported
	PoolUpstreamInfoStatusFailed      = UpstreamBillingProbeStatusFailed
)

// poolUpstreamInfoIntervalMinutes is the fixed per-account refresh interval.
// The information probe has no admin-configurable interval; freshness is twice
// this value (60 minutes at the default).
const poolUpstreamInfoIntervalMinutes = upstreamBillingProbeDefaultIntervalMinutes

var (
	ErrPoolUpstreamInfoUnavailable = infraerrors.ServiceUnavailable(
		"POOL_UPSTREAM_INFO_UNAVAILABLE", "pool upstream info probe is unavailable",
	)
	ErrPoolUpstreamInfoAccountInvalid = infraerrors.BadRequest(
		"POOL_UPSTREAM_INFO_ACCOUNT_INVALID",
		"pool upstream info probe requires an API-key account with pool mode enabled",
	)
	ErrPoolUpstreamInfoNotConfigured = infraerrors.BadRequest(
		"POOL_UPSTREAM_INFO_NOT_CONFIGURED",
		"pool upstream info probe is not configured on this account",
	)
	ErrPoolUpstreamInfoIdentityChanged = infraerrors.Conflict(
		"POOL_UPSTREAM_INFO_IDENTITY_CHANGED",
		"account identity or pool upstream configuration changed during the probe; retry the probe",
	)
	ErrPoolUpstreamInfoInvalidPlatform = infraerrors.BadRequest(
		"POOL_UPSTREAM_INFO_INVALID_PLATFORM",
		"pool_upstream_platform must be one of default, sub2api, chatgpt2api",
	)
	ErrPoolUpstreamInfoInvalidFeatures = infraerrors.BadRequest(
		"POOL_UPSTREAM_INFO_INVALID_FEATURES",
		"pool_upstream_features must be an array of supported feature names",
	)
	ErrPoolUpstreamInfoInvalidCombination = infraerrors.BadRequest(
		"POOL_UPSTREAM_INFO_INVALID_COMBINATION",
		"pool upstream platform and features are not a supported combination for this account",
	)
)

// PoolUpstreamInfoSnapshot is the sanitized record persisted in
// accounts.extra[pool_upstream_info]. It never stores raw upstream responses,
// headers, or credentials.
type PoolUpstreamInfoSnapshot struct {
	Status        string         `json:"status"`
	Platform      string         `json:"platform,omitempty"`
	Features      []string       `json:"features,omitempty"`
	Data          map[string]any `json:"data,omitempty"`
	ReceivedAt    *time.Time     `json:"received_at,omitempty"`
	FreshUntil    *time.Time     `json:"fresh_until,omitempty"`
	LastAttemptAt time.Time      `json:"last_attempt_at"`
	NextProbeAt   time.Time      `json:"next_probe_at"`
	FailureCount  int            `json:"failure_count,omitempty"`
	HTTPStatus    int            `json:"http_status,omitempty"`
	LastError     string         `json:"last_error,omitempty"`
}

// PoolUpstreamInfoResult is returned by the manual refresh endpoint.
type PoolUpstreamInfoResult struct {
	AccountID int64                     `json:"account_id"`
	Snapshot  *PoolUpstreamInfoSnapshot `json:"snapshot,omitempty"`
	Error     string                    `json:"error,omitempty"`
}

// poolUpstreamSupportedFeatures lists the selectable features per platform.
func poolUpstreamSupportedFeatures(platform string) []string {
	switch platform {
	case PoolUpstreamPlatformSub2API:
		return []string{PoolUpstreamFeatureBalance}
	case PoolUpstreamPlatformChatGPT2API:
		return []string{PoolUpstreamFeatureAccountCount, PoolUpstreamFeatureImageQuota}
	default:
		return nil
	}
}

// poolUpstreamFeatureList normalizes a decoded extra value into a sorted,
// deduplicated feature list. Non-array input yields nil so tolerant readers
// collapse malformed state to "no features".
func poolUpstreamFeatureList(value any) []string {
	var raw []any
	switch typed := value.(type) {
	case []string:
		raw = make([]any, 0, len(typed))
		for _, item := range typed {
			raw = append(raw, item)
		}
	case []any:
		raw = typed
	default:
		return nil
	}
	seen := make(map[string]struct{}, len(raw))
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		name, ok := item.(string)
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	slices.Sort(out)
	return out
}

// PoolUpstreamSelection reads the declared selection tolerantly: absent or
// malformed values collapse to the default platform with no features and
// unknown or platform-incompatible feature names are dropped. Write paths use
// the strict validation in resolvePoolUpstreamSelection instead.
func PoolUpstreamSelection(extra map[string]any) (string, []string) {
	if extra == nil {
		return PoolUpstreamPlatformDefault, nil
	}
	platform, _ := extra[PoolUpstreamPlatformExtraKey].(string)
	platform = strings.TrimSpace(platform)
	switch platform {
	case PoolUpstreamPlatformSub2API, PoolUpstreamPlatformChatGPT2API:
	default:
		platform = PoolUpstreamPlatformDefault
	}
	if platform == PoolUpstreamPlatformDefault {
		return platform, nil
	}
	supported := poolUpstreamSupportedFeatures(platform)
	features := make([]string, 0, len(supported))
	for _, feature := range poolUpstreamFeatureList(extra[PoolUpstreamFeaturesExtraKey]) {
		if slices.Contains(supported, feature) {
			features = append(features, feature)
		}
	}
	slices.Sort(features)
	return platform, features
}

// poolUpstreamSelectionUpdate is the parsed client-provided selection from an
// account create/edit request extra map.
type poolUpstreamSelectionUpdate struct {
	platformProvided bool
	platform         string
	featuresProvided bool
	features         []string
}

func (u poolUpstreamSelectionUpdate) provided() bool {
	return u.platformProvided || u.featuresProvided
}

// extractPoolUpstreamSelectionUpdate removes and strictly parses the pool
// upstream config keys from a client-supplied extra map. Unknown enum values
// and wrong types are rejected rather than silently corrected.
func extractPoolUpstreamSelectionUpdate(extra map[string]any) (poolUpstreamSelectionUpdate, error) {
	var update poolUpstreamSelectionUpdate
	if extra == nil {
		return update, nil
	}
	if raw, exists := extra[PoolUpstreamPlatformExtraKey]; exists {
		update.platformProvided = true
		if raw == nil {
			update.platform = PoolUpstreamPlatformDefault
		} else {
			platform, ok := raw.(string)
			if !ok {
				return update, ErrPoolUpstreamInfoInvalidPlatform
			}
			platform = strings.TrimSpace(platform)
			switch platform {
			case "", PoolUpstreamPlatformDefault:
				update.platform = PoolUpstreamPlatformDefault
			case PoolUpstreamPlatformSub2API, PoolUpstreamPlatformChatGPT2API:
				update.platform = platform
			default:
				return update, ErrPoolUpstreamInfoInvalidPlatform
			}
		}
	}
	if raw, exists := extra[PoolUpstreamFeaturesExtraKey]; exists {
		update.featuresProvided = true
		if raw == nil {
			update.features = []string{}
		} else {
			var items []any
			switch typed := raw.(type) {
			case []string:
				items = make([]any, 0, len(typed))
				for _, item := range typed {
					items = append(items, item)
				}
			case []any:
				items = typed
			default:
				return update, ErrPoolUpstreamInfoInvalidFeatures
			}
			seen := make(map[string]struct{}, len(items))
			features := make([]string, 0, len(items))
			for _, item := range items {
				name, ok := item.(string)
				if !ok {
					return update, ErrPoolUpstreamInfoInvalidFeatures
				}
				name = strings.TrimSpace(name)
				switch name {
				case PoolUpstreamFeatureBalance, PoolUpstreamFeatureAccountCount, PoolUpstreamFeatureImageQuota:
				default:
					return update, ErrPoolUpstreamInfoInvalidFeatures
				}
				if _, exists := seen[name]; exists {
					continue
				}
				seen[name] = struct{}{}
				features = append(features, name)
			}
			slices.Sort(features)
			update.features = features
		}
	}
	delete(extra, PoolUpstreamPlatformExtraKey)
	delete(extra, PoolUpstreamFeaturesExtraKey)
	delete(extra, PoolUpstreamInfoExtraKey)
	return update, nil
}

// isPoolUpstreamInfoEligible reports whether the account state may hold a
// non-default pool upstream selection: API-key type with pool mode enabled.
// Bedrock pool mode is deliberately out of scope.
func isPoolUpstreamInfoEligible(account *Account) bool {
	return account != nil && account.Type == AccountTypeAPIKey && account.IsPoolMode()
}

// isPoolUpstreamInfoProbeAccount reports whether the account has a probeable
// pool upstream selection right now.
func isPoolUpstreamInfoProbeAccount(account *Account) bool {
	if !isPoolUpstreamInfoEligible(account) {
		return false
	}
	platform, features := PoolUpstreamSelection(account.Extra)
	return platform != PoolUpstreamPlatformDefault && len(features) > 0
}

// resolvePoolUpstreamSelection computes the normalized selection to persist for
// an account whose credentials/type were already merged with the request.
// storedExtra is the pre-update extra (nil on create). Explicitly provided
// selections are validated and rejected on invalid enum, combination, type, or
// pool-mode prerequisites; a stored selection that becomes ineligible (pool
// mode turned off, type changed away from apikey) is normalized to default+[].
func resolvePoolUpstreamSelection(
	account *Account,
	update poolUpstreamSelectionUpdate,
	storedExtra map[string]any,
) (string, []string, error) {
	storedPlatform, storedFeatures := PoolUpstreamSelection(storedExtra)

	platform := storedPlatform
	if update.platformProvided {
		platform = update.platform
	}
	features := storedFeatures
	// Switching the declared platform drops incompatible features unless the
	// request supplied an explicit replacement list.
	if update.platformProvided && platform != storedPlatform {
		supported := poolUpstreamSupportedFeatures(platform)
		filtered := features[:0]
		for _, feature := range features {
			if slices.Contains(supported, feature) {
				filtered = append(filtered, feature)
			}
		}
		features = filtered
	}
	if update.featuresProvided {
		features = update.features
	}

	eligible := isPoolUpstreamInfoEligible(account)
	if !eligible {
		// An explicitly provided non-default selection under a pool-mode/type
		// state that cannot hold it is rejected; a stored selection that became
		// ineligible is normalized to default+[].
		if update.provided() && (platform != PoolUpstreamPlatformDefault || len(features) > 0) {
			return "", nil, ErrPoolUpstreamInfoAccountInvalid
		}
		return PoolUpstreamPlatformDefault, nil, nil
	}

	switch platform {
	case PoolUpstreamPlatformDefault:
		if len(features) > 0 {
			return "", nil, ErrPoolUpstreamInfoInvalidCombination
		}
		return platform, nil, nil
	case PoolUpstreamPlatformSub2API:
		// Available on any API-key platform.
	case PoolUpstreamPlatformChatGPT2API:
		if account.Platform != PlatformOpenAI {
			return "", nil, ErrPoolUpstreamInfoInvalidCombination
		}
	default:
		return "", nil, ErrPoolUpstreamInfoInvalidPlatform
	}
	supported := poolUpstreamSupportedFeatures(platform)
	for _, feature := range features {
		if !slices.Contains(supported, feature) {
			return "", nil, ErrPoolUpstreamInfoInvalidCombination
		}
	}
	return platform, features, nil
}

// writePoolUpstreamSelection stores the normalized selection keys. Keys are
// written whenever the selection is non-default, the request provided them, or
// the account previously stored them (so disabling pool mode persists
// default+[] instead of leaving stale config behind).
func writePoolUpstreamSelection(
	account *Account,
	platform string,
	features []string,
	update poolUpstreamSelectionUpdate,
	storedExtra map[string]any,
) {
	_, hadPlatform := storedExtra[PoolUpstreamPlatformExtraKey]
	_, hadFeatures := storedExtra[PoolUpstreamFeaturesExtraKey]
	if platform == PoolUpstreamPlatformDefault && len(features) == 0 &&
		!update.provided() && !hadPlatform && !hadFeatures {
		return
	}
	if account.Extra == nil {
		account.Extra = make(map[string]any, 2)
	}
	account.Extra[PoolUpstreamPlatformExtraKey] = platform
	stored := make([]string, len(features))
	copy(stored, features)
	account.Extra[PoolUpstreamFeaturesExtraKey] = stored
}

// poolUpstreamInfoIdentity captures every input a probe result depends on:
// upstream endpoint identity, credentials subset, and the declared selection.
// Any change resets the snapshot and fails in-flight snapshot CAS writes.
func poolUpstreamInfoIdentity(account *Account) map[string]any {
	identity := upstreamBillingProbeIdentity(account)
	if identity == nil {
		identity = make(map[string]any)
	}
	platform, features := PoolUpstreamSelection(account.Extra)
	identity["pool_upstream_platform"] = platform
	identity["pool_upstream_features"] = features
	identity["pool_mode"] = account.IsPoolMode()
	return identity
}

// poolUpstreamInfoIdentityEqual compares two identity maps. Feature lists are
// already normalized to sorted []string by PoolUpstreamSelection.
func poolUpstreamInfoIdentityEqual(left, right map[string]any) bool {
	return reflect.DeepEqual(left, right)
}

// updatesPoolUpstreamProbeIdentity reports whether a partial credentials update
// touches a field the info probe identity depends on.
func updatesPoolUpstreamProbeIdentity(credentials map[string]any) bool {
	if updatesUpstreamBillingProbeIdentity(credentials) {
		return true
	}
	_, ok := credentials["pool_mode"]
	return ok
}

// applyPoolUpstreamSelectionForCreate validates and normalizes the declared
// selection on a newly built account. The snapshot key is always dropped: it is
// probe-managed and client-provided values are ignored.
func applyPoolUpstreamSelectionForCreate(account *Account) error {
	update, err := extractPoolUpstreamSelectionUpdate(account.Extra)
	if err != nil {
		return err
	}
	platform, features, err := resolvePoolUpstreamSelection(account, update, nil)
	if err != nil {
		return err
	}
	writePoolUpstreamSelection(account, platform, features, update, nil)
	return nil
}

// poolUpstreamInfoEndpointURL converts a validated upstream base URL into the
// fixed probe endpoint for the declared platform. A reverse-proxy path prefix
// is preserved while a trailing API version segment (/v1 or /api/v1) is
// removed, so a base of https://host/proxy/v1 probes
// https://host/proxy/api/dashboard rather than .../v1/api/dashboard.
// Query strings, fragments and userinfo are stripped: the probe endpoint is
// fixed and credentials travel in the Authorization header only.
func poolUpstreamInfoEndpointURL(normalizedBase, platform string) (string, error) {
	parsed, err := url.Parse(normalizedBase)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("invalid upstream base url")
	}
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""
	parsed.User = nil

	var endpoint string
	switch platform {
	case PoolUpstreamPlatformSub2API:
		endpoint = "/v1/usage"
	case PoolUpstreamPlatformChatGPT2API:
		endpoint = "/api/dashboard"
	default:
		return "", fmt.Errorf("unsupported pool upstream platform: %s", platform)
	}

	path := parsed.Path
	lower := strings.ToLower(path)
	switch {
	case strings.HasSuffix(lower, "/api/v1"):
		path = path[:len(path)-len("/api/v1")]
	case strings.HasSuffix(lower, "/v1"):
		path = path[:len(path)-len("/v1")]
	}
	path = strings.TrimRight(path, "/")
	parsed.Path = path + endpoint
	parsed.RawPath = ""
	return parsed.String(), nil
}

// nonNegativeFiniteNumber extracts a finite non-negative JSON number.
func nonNegativeFiniteNumber(value any) (float64, bool) {
	var number float64
	switch typed := value.(type) {
	case json.Number:
		parsed, err := typed.Float64()
		if err != nil {
			return 0, false
		}
		number = parsed
	case float64:
		number = typed
	case float32:
		number = float64(typed)
	case int:
		number = float64(typed)
	case int64:
		number = float64(typed)
	default:
		return 0, false
	}
	if math.IsNaN(number) || math.IsInf(number, 0) || number < 0 {
		return 0, false
	}
	return number, true
}

// nonNegativeInt64 extracts a non-negative integer JSON number.
func nonNegativeInt64(value any) (int64, bool) {
	var number int64
	switch typed := value.(type) {
	case json.Number:
		if parsed, err := typed.Int64(); err == nil {
			number = parsed
		} else if asFloat, ferr := typed.Float64(); ferr == nil && asFloat == math.Trunc(asFloat) {
			number = int64(asFloat)
		} else {
			return 0, false
		}
	case float64:
		if typed != math.Trunc(typed) {
			return 0, false
		}
		number = int64(typed)
	case int64:
		number = typed
	case int:
		number = int64(typed)
	default:
		return 0, false
	}
	if number < 0 {
		return 0, false
	}
	return number, true
}

func decodeJSONMap(body []byte) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var payload map[string]any
	if err := decoder.Decode(&payload); err != nil {
		return nil, err
	}
	if payload == nil {
		return nil, fmt.Errorf("empty response")
	}
	if decoder.More() {
		return nil, fmt.Errorf("unexpected trailing data")
	}
	return payload, nil
}

// parsePoolUpstreamSub2APIUsage projects a GET /v1/usage response into the
// sanitized balance data. mode=unrestricted exposes a numeric `balance` as the
// USD wallet; without a balance a numeric `remaining` is only meaningful when
// a `subscription` object is present. mode=quota_limited exposes numeric
// `remaining` as USD key quota. Unknown modes, missing or invalid values, and
// non-USD units are errors, never zeros.
func parsePoolUpstreamSub2APIUsage(body []byte) (map[string]any, error) {
	payload, err := decodeJSONMap(body)
	if err != nil {
		return nil, err
	}
	unit, _ := payload["unit"].(string)
	if unit != "USD" {
		return nil, fmt.Errorf("unexpected or missing unit")
	}
	switch mode, _ := payload["mode"].(string); mode {
	case "unrestricted":
		if rawBalance, present := payload["balance"]; present {
			balance, ok := nonNegativeFiniteNumber(rawBalance)
			if !ok {
				return nil, fmt.Errorf("invalid balance")
			}
			return map[string]any{"kind": "wallet", "amount_usd": balance}, nil
		}
		if _, isSubscription := payload["subscription"].(map[string]any); !isSubscription {
			return nil, fmt.Errorf("unrestricted response without balance or subscription")
		}
		remaining, ok := nonNegativeFiniteNumber(payload["remaining"])
		if !ok {
			return nil, fmt.Errorf("invalid remaining")
		}
		return map[string]any{"kind": "subscription", "amount_usd": remaining}, nil
	case "quota_limited":
		remaining, ok := nonNegativeFiniteNumber(payload["remaining"])
		if !ok {
			return nil, fmt.Errorf("invalid or missing remaining")
		}
		return map[string]any{"kind": "quota", "amount_usd": remaining}, nil
	default:
		return nil, fmt.Errorf("unsupported or missing mode")
	}
}

// parsePoolUpstreamChatGPT2APIDashboard projects a GET /api/dashboard response
// into sanitized counts. `accounts.active` is required when account_count was
// selected, `accounts.total_quota` when image_quota was selected; the optional
// counters are stored only when present and valid. Missing or invalid required
// fields are errors, never zeros.
func parsePoolUpstreamChatGPT2APIDashboard(body []byte, features []string) (map[string]any, error) {
	payload, err := decodeJSONMap(body)
	if err != nil {
		return nil, err
	}
	accounts, ok := payload["accounts"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("missing accounts object")
	}
	data := make(map[string]any, 4)
	if slices.Contains(features, PoolUpstreamFeatureAccountCount) {
		active, ok := nonNegativeInt64(accounts["active"])
		if !ok {
			return nil, fmt.Errorf("invalid accounts.active")
		}
		data["accounts_active"] = active
	}
	if slices.Contains(features, PoolUpstreamFeatureImageQuota) {
		totalQuota, ok := nonNegativeInt64(accounts["total_quota"])
		if !ok {
			return nil, fmt.Errorf("invalid accounts.total_quota")
		}
		data["total_quota"] = totalQuota
		// The quota-breakdown counters are only meaningful when image_quota was
		// selected; they are optional and skipped when absent or invalid.
		for _, key := range []string{"unlimited_quota_count", "unknown_quota_count"} {
			raw, present := accounts[key]
			if !present || raw == nil {
				continue
			}
			if value, ok := nonNegativeInt64(raw); ok {
				data[key] = value
			}
		}
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("no selected feature present in response")
	}
	return data, nil
}
