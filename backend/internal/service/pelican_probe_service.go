package service

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/tidwall/gjson"
)

// PelicanProbeService provides CRUD + execution for group-level IQ probes.
//
// MVP design: probes self-call the user-facing OpenAI-compatible endpoint
// (POST {base_url}/v1/chat/completions) with a probe API key bound to the
// target group, so results reflect the real scheduling chain (routing, account
// pool, rate limits). Verdicts are human-reviewed; no VLM judge yet.
// PelicanProbeEnabledSettingKey is the global on/off switch (settings KV).
// Missing key means enabled.
const PelicanProbeEnabledSettingKey = "pelican_probe_enabled"

// Probe run budgets.
const (
	// pelicanProbeRunTimeout bounds one probe run. Manual ("run now") and
	// scheduled runs share the same budget: generating an SVG animation
	// legitimately takes minutes, so the ceiling is generous.
	pelicanProbeRunTimeout = 10 * time.Minute
	// pelicanProbeClientTimeout is the HTTP client budget for one probe request.
	// It matches the run budget on purpose: the request starts after the run
	// context was created, so the context deadline always fires first and the
	// client timeout is only a backstop (it must never cut a long stream short
	// before the context says the budget is over).
	pelicanProbeClientTimeout = 10 * time.Minute
	// pelicanProbePersistenceTimeout bounds the DB writes after a probe finished.
	pelicanProbePersistenceTimeout = 15 * time.Second
)

// Sentinel errors of the probe execution API.
var (
	// ErrPelicanProbeAlreadyRunning reports that a run for the same plan is
	// already in flight. The admin handler maps it to HTTP 409.
	ErrPelicanProbeAlreadyRunning = errors.New("a probe run is already running for this plan")
	// ErrPelicanProbePlanNotFound reports a missing plan (mapped to HTTP 404).
	ErrPelicanProbePlanNotFound = errors.New("pelican probe plan not found")
)

// PelicanProbeRunTrigger is the payload of an accepted ("run now") trigger.
type PelicanProbeRunTrigger struct {
	Started bool  `json:"started"`
	PlanID  int64 `json:"plan_id"`
	// Reason explains a non-started trigger; empty when Started is true.
	Reason string `json:"reason,omitempty"`
}

// pelicanProbeRunGuard is a process-wide in-flight set allowing at most one
// probe run per plan at a time. It is intentionally in-memory: the API runs as a
// single process, so a distributed lease is not needed (and a multi-instance
// deployment would need a DB/redis lock instead).
type pelicanProbeRunGuard struct {
	mu      sync.Mutex
	running map[int64]struct{}
}

// tryStart reserves a run slot for planID; it reports false when one is taken.
func (g *pelicanProbeRunGuard) tryStart(planID int64) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.running == nil {
		g.running = make(map[int64]struct{})
	}
	if _, busy := g.running[planID]; busy {
		return false
	}
	g.running[planID] = struct{}{}
	return true
}

// finish releases the run slot for planID.
func (g *pelicanProbeRunGuard) finish(planID int64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.running, planID)
}

type PelicanProbeService struct {
	planRepo    PelicanProbePlanRepository
	resultRepo  PelicanProbeResultRepository
	encryptor   SecretEncryptor
	settingRepo SettingRepository
	cfg         *config.Config
	httpClient  *http.Client

	// runGuard serializes runs per plan (manual trigger + scheduled runner).
	runGuard pelicanProbeRunGuard
	// retryBackoffLadder overrides pelicanProbeRetryBackoffs when set (tests).
	retryBackoffLadder []time.Duration
	// judge holds the optional lookups used to validate the candy judge's
	// routing (judge key group / group platform / model availability).
	judge PelicanJudgeEnv
}

// NewPelicanProbeService creates a new PelicanProbeService.
func NewPelicanProbeService(
	planRepo PelicanProbePlanRepository,
	resultRepo PelicanProbeResultRepository,
	encryptor SecretEncryptor,
	settingRepo SettingRepository,
	cfg *config.Config,
	judge PelicanJudgeEnv,
) *PelicanProbeService {
	return &PelicanProbeService{
		planRepo:    planRepo,
		resultRepo:  resultRepo,
		encryptor:   encryptor,
		settingRepo: settingRepo,
		cfg:         cfg,
		httpClient:  &http.Client{Timeout: pelicanProbeClientTimeout},
		judge:       judge,
	}
}

// normalizePelicanTestType canonicalizes the plan test type.
// Empty means the default pelican test.
func normalizePelicanTestType(raw string) (string, error) {
	t := strings.ToLower(strings.TrimSpace(raw))
	if t == "" {
		return PelicanTestTypePelican, nil
	}
	switch t {
	case PelicanTestTypePelican, PelicanTestTypeCandy:
		return t, nil
	default:
		return "", fmt.Errorf("invalid test_type %q: must be %q or %q", raw, PelicanTestTypePelican, PelicanTestTypeCandy)
	}
}

// normalizePelicanReasoningEffort canonicalizes the plan effort option.
// Empty means "not specified" (field omitted from the probe request).
func normalizePelicanReasoningEffort(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", nil
	}
	normalized := NormalizeMaxReasoningEffort(raw)
	if normalized == "" {
		return "", fmt.Errorf("invalid reasoning_effort %q: must be one of minimal/low/medium/high/xhigh/max", raw)
	}
	return normalized, nil
}

// --- candy LLM judge ---

// judge verdict words returned by the judge model.
const (
	PelicanJudgeYes = "YES"
	PelicanJudgeNo  = "NO"
)

// parseJudgeVerdict extracts the judge verdict from the raw model reply.
//
// Matching is case-insensitive and requires a standalone YES / NO word (not a
// substring of a longer token such as "YESNO" or "NOPE"). An empty string is
// returned when the reply cannot be decided: neither word present, or both
// present (an ambiguous reply is never guessed at).
func parseJudgeVerdict(reply string) string {
	hasYes := containsStandaloneWord(reply, PelicanJudgeYes)
	hasNo := containsStandaloneWord(reply, PelicanJudgeNo)
	switch {
	case hasYes && hasNo:
		return ""
	case hasYes:
		return PelicanJudgeYes
	case hasNo:
		return PelicanJudgeNo
	default:
		return ""
	}
}

// containsStandaloneWord reports whether word appears in s as a standalone
// ASCII-alphabetic token (case-insensitive). Non-letter bytes such as CJK
// characters count as boundaries, so "答案是YES。" still matches.
func containsStandaloneWord(s, word string) bool {
	upper := strings.ToUpper(s)
	word = strings.ToUpper(word)
	from := 0
	for {
		idx := strings.Index(upper[from:], word)
		if idx < 0 {
			return false
		}
		start := from + idx
		end := start + len(word)
		if !isASCIILetter(upper, start-1) && !isASCIILetter(upper, end) {
			return true
		}
		from = start + 1
		if from >= len(upper) {
			return false
		}
	}
}

// isASCIILetter reports whether the byte at i is an ASCII letter; out-of-range
// indexes are boundaries.
func isASCIILetter(s string, i int) bool {
	if i < 0 || i >= len(s) {
		return false
	}
	c := s[i]
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// truncateJudgeCandidate caps the candidate answer embedded in the judge
// prompt. Counting runes (not bytes) keeps multi-byte text intact.
func truncateJudgeCandidate(s string) string {
	r := []rune(s)
	if len(r) <= PelicanJudgeCandidateMaxRunes {
		return s
	}
	return string(r[:PelicanJudgeCandidateMaxRunes])
}

// judgeNoteSnippet collapses whitespace and shortens a raw judge reply for
// storage in the result note.
func judgeNoteSnippet(s string) string {
	snippet := strings.Join(strings.Fields(s), " ")
	const maxRunes = 120
	r := []rune(snippet)
	if len(r) > maxRunes {
		return string(r[:maxRunes]) + "…"
	}
	return snippet
}

// judgeCandyAnswer asks an LLM judge whether the candy-test candidate answer
// settles on 21. It reuses the probe key and the plan's base URL so the judge
// call travels the same gateway chain (each candy run therefore costs two
// upstream calls). No max-token parameter is sent: parameter names differ
// across upstream models, so the prompt constrains the reply to one word.
//
// The judge stays non-streaming (it is a short, single-word reply) but runs on
// the caller's background context: runRow hands it the detached run context, so
// the judge survives the request that triggered the run.
//
// Return contract:
//   - verdict is PelicanJudgeYes / PelicanJudgeNo when the reply is decidable,
//     or "" when the reply cannot be decided (ambiguous or off-topic).
//   - note carries a short excerpt of the raw judge reply ("" when unavailable).
//   - err is non-nil for transport / non-2xx / empty-content failures.
func (s *PelicanProbeService) judgeCandyAnswer(ctx context.Context, baseURL, apiKey, judgeModel, candidate string) (verdict string, note string, err error) {
	judgeModel = strings.TrimSpace(judgeModel)
	if judgeModel == "" {
		return "", "", fmt.Errorf("judge model not configured")
	}
	baseURL = strings.TrimSuffix(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return "", "", fmt.Errorf("judge base url not configured")
	}
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return "", "", fmt.Errorf("judge api key not configured")
	}

	prompt := fmt.Sprintf(DefaultCandyJudgePrompt, truncateJudgeCandidate(candidate))
	reqBody := map[string]any{
		"model":       judgeModel,
		"stream":      false,
		"temperature": 0,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
	}
	bodyBytes, _ := json.Marshal(reqBody)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/v1/chat/completions", bytes.NewReader(bodyBytes))
	if err != nil {
		return "", "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := s.httpClient.Do(httpReq)
	if err != nil {
		return "", "", err
	}
	defer func() { _ = resp.Body.Close() }()
	respBytes, err := io.ReadAll(io.LimitReader(resp.Body, pelicanProbeMaxContentBytes))
	if err != nil {
		return "", "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", "", fmt.Errorf("judge HTTP %d: %s", resp.StatusCode, truncateProbeSnippet(string(respBytes)))
	}
	content := gjson.GetBytes(respBytes, "choices.0.message.content").String()
	if strings.TrimSpace(content) == "" {
		return "", "", fmt.Errorf("empty judge content")
	}
	return parseJudgeVerdict(content), judgeNoteSnippet(content), nil
}

// applyCandyJudge runs the LLM judge for a successful candy probe and folds the
// outcome into the result. The probe itself succeeded either way: a judge that
// cannot decide leaves the verdict at pending for human review instead of
// failing the run.
func (s *PelicanProbeService) applyCandyJudge(ctx context.Context, row *PelicanProbePlanRow, result *PelicanProbeResult, rawHTML string) {
	judgeModel := strings.TrimSpace(row.JudgeModelID)
	if judgeModel == "" {
		// Historical plans created before judge support: skip instead of
		// guessing, and never fail the probe over a missing judge config.
		result.Note = "auto-judge skipped: judge_model_id not configured (awaiting manual review)"
		return
	}
	baseURL, apiKey, err := s.resolveProbeEndpoint(row)
	if err != nil {
		result.Note = "auto-judge skipped: " + err.Error()
		return
	}
	// The judge may need to travel a different gateway chain than the probe: the
	// judge model is often served by another group/platform, so an unbound judge
	// call would be routed into the probe group and 404. A dedicated judge
	// gateway and/or judge key therefore overrides the probe endpoint here;
	// both default to the probe values when unset.
	if judgeBaseURL := strings.TrimSuffix(strings.TrimSpace(row.JudgeBaseURL), "/"); judgeBaseURL != "" {
		baseURL = judgeBaseURL
	}
	if strings.TrimSpace(row.JudgeAPIKeyEncrypted) != "" {
		decrypted, derr := s.encryptor.Decrypt(row.JudgeAPIKeyEncrypted)
		if derr != nil {
			result.Note = "auto-judge skipped: decrypt judge api key: " + derr.Error()
			return
		}
		if decrypted = strings.TrimSpace(decrypted); decrypted != "" {
			apiKey = decrypted
		}
	}
	verdict, note, err := s.judgeCandyAnswer(ctx, baseURL, apiKey, judgeModel, rawHTML)
	if err != nil {
		result.Note = "auto-judge skipped: " + err.Error()
		return
	}
	switch verdict {
	case PelicanJudgeYes:
		result.Verdict = PelicanVerdictPass
	case PelicanJudgeNo:
		result.Verdict = PelicanVerdictDegraded
	default:
		reason := "judge reply not a single YES/NO"
		if note != "" {
			reason = "judge reply not a single YES/NO (reply: " + note + ")"
		}
		result.Note = "auto-judge skipped: " + reason
		return
	}
	now := time.Now()
	result.ReviewedBy = PelicanAutoReviewer
	result.ReviewedAt = &now
	result.Note = fmt.Sprintf("judge=%s: %s", judgeModel, verdict)
	if note != "" {
		result.Note += " | reply: " + note
	}
}

// --- plan CRUD ---

// CreatePlanInput carries plan creation fields (APIKey is plaintext, stored encrypted).
type CreatePlanInput struct {
	Name            string
	GroupID         int64
	ModelID         string
	BaseURL         string
	APIKey          string
	Prompt          string
	TestType        string
	ReasoningEffort string
	JudgeModelID    string
	// JudgeAPIKey is the plaintext judge key (candy only; "" = reuse the probe
	// key). It is encrypted at rest and never echoed back.
	JudgeAPIKey    string
	JudgeBaseURL   string
	CronExpression string
	Enabled        bool
	IsPublic       bool
	MaxResults     int
}

// CreatePlan validates inputs, encrypts the probe key, and persists the plan.
func (s *PelicanProbeService) CreatePlan(ctx context.Context, in CreatePlanInput) (*PelicanProbePlan, error) {
	if in.GroupID <= 0 {
		return nil, fmt.Errorf("group_id is required")
	}
	if strings.TrimSpace(in.ModelID) == "" {
		return nil, fmt.Errorf("model_id is required")
	}
	if strings.TrimSpace(in.APIKey) == "" {
		return nil, fmt.Errorf("api_key is required (probe key bound to the target group)")
	}
	cronExpr := strings.TrimSpace(in.CronExpression)
	if cronExpr == "" {
		cronExpr = "0 */4 * * *"
	}
	nextRun, err := computeNextRun(cronExpr, time.Now())
	if err != nil {
		return nil, fmt.Errorf("invalid cron expression: %w", err)
	}
	prompt := strings.TrimSpace(in.Prompt)
	if prompt == "" {
		prompt = DefaultPelicanPrompt
	}
	testType, err := normalizePelicanTestType(in.TestType)
	if err != nil {
		return nil, err
	}
	// Candy tests always use the fixed prompt so results stay comparable.
	if testType == PelicanTestTypeCandy {
		prompt = DefaultCandyPrompt
	}
	// Candy plans must name an LLM judge: without it the run cannot be graded.
	// Pelican plans ignore the field (it is only meaningful for candy).
	judgeModelID := strings.TrimSpace(in.JudgeModelID)
	if testType == PelicanTestTypeCandy && judgeModelID == "" {
		return nil, fmt.Errorf("judge_model_id is required when test_type is %q", PelicanTestTypeCandy)
	}
	// A judge model that the judge call cannot reach would fail on every run and
	// leave the verdict pending forever, so reject the plan up front.
	judgeAPIKey := strings.TrimSpace(in.JudgeAPIKey)
	judgeBaseURL := strings.TrimSpace(in.JudgeBaseURL)
	if testType == PelicanTestTypeCandy {
		if err := s.validateCandyJudgeModel(ctx, in.GroupID, judgeModelID, judgeAPIKey); err != nil {
			return nil, err
		}
	}
	effort, err := normalizePelicanReasoningEffort(in.ReasoningEffort)
	if err != nil {
		return nil, err
	}
	maxResults := in.MaxResults
	if maxResults <= 0 {
		maxResults = 50
	}
	encrypted, err := s.encryptor.Encrypt(strings.TrimSpace(in.APIKey))
	if err != nil {
		return nil, fmt.Errorf("encrypt api key: %w", err)
	}
	// The judge key is candy-only; encrypting it here keeps the plaintext inside
	// this function (the row is the only place it is stored).
	judgeKeyEncrypted := ""
	if testType == PelicanTestTypeCandy && judgeAPIKey != "" {
		judgeKeyEncrypted, err = s.encryptor.Encrypt(judgeAPIKey)
		if err != nil {
			return nil, fmt.Errorf("encrypt judge api key: %w", err)
		}
	}
	row := &PelicanProbePlanRow{
		PelicanProbePlan: PelicanProbePlan{
			Name:            strings.TrimSpace(in.Name),
			GroupID:         in.GroupID,
			ModelID:         strings.TrimSpace(in.ModelID),
			BaseURL:         strings.TrimSpace(in.BaseURL),
			TestType:        testType,
			ReasoningEffort: effort,
			JudgeModelID:    judgeModelID,
			JudgeBaseURL:    judgeBaseURL,
			CronExpression:  cronExpr,
			Enabled:         in.Enabled,
			IsPublic:        in.IsPublic,
			MaxResults:      maxResults,
			NextRunAt:       &nextRun,
		},
		Prompt:               prompt,
		APIKeyEncrypted:      encrypted,
		JudgeAPIKeyEncrypted: judgeKeyEncrypted,
	}
	created, err := s.planRepo.Create(ctx, row)
	if err != nil {
		return nil, err
	}
	return sanitizePlan(created), nil
}

// UpdatePlanInput carries updatable plan fields (nil/empty = keep existing;
// APIKey empty = keep existing key).
type UpdatePlanInput struct {
	Name            *string
	ModelID         *string
	BaseURL         *string
	APIKey          string
	Prompt          *string
	TestType        *string
	ReasoningEffort *string
	JudgeModelID    *string
	// JudgeAPIKey empty = keep the stored judge key; non-empty = replace it.
	JudgeAPIKey    string
	JudgeBaseURL   *string
	CronExpression *string
	Enabled        *bool
	IsPublic       *bool
	MaxResults     *int
}

// UpdatePlan applies partial updates and recomputes next_run_at on cron change.
func (s *PelicanProbeService) UpdatePlan(ctx context.Context, id int64, in UpdatePlanInput) (*PelicanProbePlan, error) {
	existing, err := s.planRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if in.Name != nil {
		existing.Name = strings.TrimSpace(*in.Name)
	}
	if in.ModelID != nil && strings.TrimSpace(*in.ModelID) != "" {
		existing.ModelID = strings.TrimSpace(*in.ModelID)
	}
	if in.BaseURL != nil {
		existing.BaseURL = strings.TrimSpace(*in.BaseURL)
	}
	if in.JudgeBaseURL != nil {
		existing.JudgeBaseURL = strings.TrimSpace(*in.JudgeBaseURL)
	}
	if strings.TrimSpace(in.APIKey) != "" {
		encrypted, err := s.encryptor.Encrypt(strings.TrimSpace(in.APIKey))
		if err != nil {
			return nil, fmt.Errorf("encrypt api key: %w", err)
		}
		existing.APIKeyEncrypted = encrypted
	}
	// An empty judge_api_key means "keep the stored one", mirroring api_key.
	if strings.TrimSpace(in.JudgeAPIKey) != "" {
		encrypted, err := s.encryptor.Encrypt(strings.TrimSpace(in.JudgeAPIKey))
		if err != nil {
			return nil, fmt.Errorf("encrypt judge api key: %w", err)
		}
		existing.JudgeAPIKeyEncrypted = encrypted
	}
	if in.Prompt != nil && strings.TrimSpace(*in.Prompt) != "" {
		existing.Prompt = strings.TrimSpace(*in.Prompt)
	}
	if in.TestType != nil && strings.TrimSpace(*in.TestType) != "" {
		testType, err := normalizePelicanTestType(*in.TestType)
		if err != nil {
			return nil, err
		}
		// Switching away from candy drops the pinned fixed prompt so the plan
		// falls back to the pelican default (unless a custom prompt is sent).
		if existing.TestType == PelicanTestTypeCandy && testType != PelicanTestTypeCandy &&
			(in.Prompt == nil || strings.TrimSpace(*in.Prompt) == "") {
			existing.Prompt = DefaultPelicanPrompt
		}
		existing.TestType = testType
	}
	// Candy plans always pin the fixed prompt, even if a custom one was sent.
	if existing.TestType == PelicanTestTypeCandy {
		existing.Prompt = DefaultCandyPrompt
		// The judge model only applies to candy plans; for pelican plans the
		// field is ignored so a stored value survives a round trip.
		if in.JudgeModelID != nil {
			existing.JudgeModelID = strings.TrimSpace(*in.JudgeModelID)
		}
	}
	// Validate the candy judge requirement whenever the caller touches test
	// configuration. Updates that only flip enabled/is_public are exempt so a
	// legacy candy plan with no judge model can still be toggled (it simply
	// keeps running with the pending fallback).
	if (in.TestType != nil || in.JudgeModelID != nil) &&
		existing.TestType == PelicanTestTypeCandy && strings.TrimSpace(existing.JudgeModelID) == "" {
		return nil, fmt.Errorf("judge_model_id is required when test_type is %q", PelicanTestTypeCandy)
	}
	// Same routing check as create: whenever the caller touches the candy judge
	// configuration, the judge model must be reachable from the group the judge
	// call will be routed to. Updates that only flip enabled/is_public are exempt
	// (a legacy plan must stay toggleable).
	if (in.TestType != nil || in.JudgeModelID != nil || strings.TrimSpace(in.JudgeAPIKey) != "") &&
		existing.TestType == PelicanTestTypeCandy && strings.TrimSpace(existing.JudgeModelID) != "" {
		effectiveJudgeKey := strings.TrimSpace(in.JudgeAPIKey)
		if effectiveJudgeKey == "" && strings.TrimSpace(existing.JudgeAPIKeyEncrypted) != "" {
			if decrypted, derr := s.encryptor.Decrypt(existing.JudgeAPIKeyEncrypted); derr == nil {
				effectiveJudgeKey = strings.TrimSpace(decrypted)
			}
		}
		if err := s.validateCandyJudgeModel(ctx, existing.GroupID, existing.JudgeModelID, effectiveJudgeKey); err != nil {
			return nil, err
		}
	}
	if in.ReasoningEffort != nil {
		effort, err := normalizePelicanReasoningEffort(*in.ReasoningEffort)
		if err != nil {
			return nil, err
		}
		existing.ReasoningEffort = effort
	}
	if in.CronExpression != nil && strings.TrimSpace(*in.CronExpression) != "" {
		existing.CronExpression = strings.TrimSpace(*in.CronExpression)
	}
	if in.Enabled != nil {
		existing.Enabled = *in.Enabled
	}
	if in.IsPublic != nil {
		existing.IsPublic = *in.IsPublic
	}
	if in.MaxResults != nil && *in.MaxResults > 0 {
		existing.MaxResults = *in.MaxResults
	}
	nextRun, err := computeNextRun(existing.CronExpression, time.Now())
	if err != nil {
		return nil, fmt.Errorf("invalid cron expression: %w", err)
	}
	existing.NextRunAt = &nextRun
	updated, err := s.planRepo.Update(ctx, existing)
	if err != nil {
		return nil, err
	}
	return sanitizePlan(updated), nil
}

// GetPlan returns a sanitized plan by ID.
func (s *PelicanProbeService) GetPlan(ctx context.Context, id int64) (*PelicanProbePlan, error) {
	row, err := s.planRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	return sanitizePlan(row), nil
}

// ListPlans returns sanitized plans, optionally filtered by group.
func (s *PelicanProbeService) ListPlans(ctx context.Context, groupID *int64) ([]*PelicanProbePlan, error) {
	rows, err := s.planRepo.List(ctx, groupID)
	if err != nil {
		return nil, err
	}
	out := make([]*PelicanProbePlan, 0, len(rows))
	for _, r := range rows {
		out = append(out, sanitizePlan(r))
	}
	return out, nil
}

// DeletePlan removes a plan and its results (via CASCADE).
func (s *PelicanProbeService) DeletePlan(ctx context.Context, id int64) error {
	return s.planRepo.Delete(ctx, id)
}

// --- global switch ---

// GetGlobalEnabled reports the master on/off switch. Missing key or read
// error defaults to enabled so transient DB issues don't kill probing.
func (s *PelicanProbeService) GetGlobalEnabled(ctx context.Context) bool {
	if s.settingRepo == nil {
		return true
	}
	val, err := s.settingRepo.GetValue(ctx, PelicanProbeEnabledSettingKey)
	if err != nil || strings.TrimSpace(val) == "" {
		return true
	}
	return !isFalseSettingValue(val)
}

// SetGlobalEnabled flips the master on/off switch.
func (s *PelicanProbeService) SetGlobalEnabled(ctx context.Context, enabled bool) error {
	if s.settingRepo == nil {
		return fmt.Errorf("setting repository unavailable")
	}
	val := "true"
	if !enabled {
		val = "false"
	}
	return s.settingRepo.Set(ctx, PelicanProbeEnabledSettingKey, val)
}

// --- public status ---

// ListPublicStatus returns the user-facing payload. When globally disabled,
// only Enabled=false is returned with empty lists.
//
// Everything returned here is user-visible, so it is redacted on the way out:
// raw upstream errors (which embed the internal probe endpoint and upstream
// bodies) are replaced by a coarse failure_reason, skip notes that quote an
// upstream error are dropped, and the probe base URL is blanked. The admin
// endpoints are untouched and keep the verbatim error text.
func (s *PelicanProbeService) ListPublicStatus(ctx context.Context) (*PelicanProbePublicStatus, error) {
	out := &PelicanProbePublicStatus{Enabled: false, Plans: []*PelicanProbePlan{}, Results: []*PelicanProbeResult{}}
	if !s.GetGlobalEnabled(ctx) {
		return out, nil
	}
	out.Enabled = true
	rows, err := s.planRepo.ListPublic(ctx)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out.Plans = append(out.Plans, sanitizePlanPublic(r))
	}
	results, err := s.resultRepo.ListPublicRecent(ctx, 60)
	if err != nil {
		return nil, err
	}
	out.Results = make([]*PelicanProbeResult, 0, len(results))
	for _, r := range results {
		out.Results = append(out.Results, publicResult(r))
	}
	return out, nil
}

// sanitizePlanPublic is sanitizePlan plus the fields that are internal-only.
//
// base_url is the probe's own endpoint (by default the loopback address of this
// server): users have no use for it and it leaks internal topology.
func sanitizePlanPublic(row *PelicanProbePlanRow) *PelicanProbePlan {
	p := sanitizePlan(row)
	p.BaseURL = ""
	return p
}

// publicResult copies a result and strips everything the user endpoint must not
// expose. The raw error is replaced by a classification; FailureReason is
// derived and never persisted.
func publicResult(r *PelicanProbeResult) *PelicanProbeResult {
	if r == nil {
		return nil
	}
	out := *r
	if strings.TrimSpace(out.ErrorMessage) != "" {
		out.FailureReason = classifyProbeFailure(out.ErrorMessage)
	}
	out.ErrorMessage = ""
	out.Note = publicResultNote(out.Note)
	return &out
}

// publicResultNote reduces a stored note to the part that is safe to publish.
//
// Whitelist, not blacklist: only the judge verdict prefix
// (`judge=<model>: YES|NO`) is exposed. Skip notes quote upstream errors and can
// therefore carry the probe URL, a path or an upstream body, so they are dropped
// entirely (the verdict already tells the user the run awaits review).
func publicResultNote(note string) string {
	note = strings.TrimSpace(note)
	if trimmed, ok := strings.CutPrefix(note, "judge="); ok {
		verdict := trimmed
		if idx := strings.Index(verdict, "|"); idx >= 0 {
			verdict = strings.TrimSpace(verdict[:idx])
		}
		return "judge=" + strings.TrimSpace(verdict)
	}
	return ""
}

// classifyProbeFailure maps a raw upstream error to the coarse enum the user
// endpoint publishes. Order matters: concurrency gates and timeouts are checked
// before the more generic HTTP classes.
func classifyProbeFailure(message string) string {
	msg := strings.ToLower(strings.TrimSpace(message))
	if msg == "" {
		return ""
	}
	switch {
	case containsAny(msg,
		"gateway_concurrency_limit", "gateway_queue_full", "concurrency limit",
		"too many requests", "rate limit", "http 429"):
		return PelicanFailureConcurrency
	case containsAny(msg,
		"context deadline exceeded", "deadline exceeded", "client.timeout",
		"timeout", "timed out", "context canceled"):
		return PelicanFailureTimeout
	case containsAny(msg,
		"model_not_found", "invalid_request", "invalid request", "bad request",
		"unauthorized", "forbidden", "api key", "decrypt", "not configured",
		"http 400", "http 401", "http 403", "http 404", "http 422"):
		return PelicanFailureConfig
	case containsAny(msg,
		"http 500", "http 502", "http 503", "http 504", "empty model content",
		"connection refused", "connection reset", "unexpected eof", "eof",
		"response too large", "stream error"):
		return PelicanFailureUpstream
	default:
		return PelicanFailureUnknown
	}
}

// --- results ---

// ListResults returns recent results for a plan.
func (s *PelicanProbeService) ListResults(ctx context.Context, planID int64, limit int) ([]*PelicanProbeResult, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	return s.resultRepo.ListByPlanID(ctx, planID, limit)
}

// ListRecentResults returns recent results across plans, optionally filtered by group.
func (s *PelicanProbeService) ListRecentResults(ctx context.Context, groupID *int64, limit int) ([]*PelicanProbeResult, error) {
	if limit <= 0 {
		limit = 60
	}
	if limit > 200 {
		limit = 200
	}
	return s.resultRepo.ListRecent(ctx, groupID, limit)
}

// ReviewResult records a human verdict on a result.
func (s *PelicanProbeService) ReviewResult(ctx context.Context, id int64, verdict, note, reviewer string) (*PelicanProbeResult, error) {
	switch verdict {
	case PelicanVerdictPass, PelicanVerdictDegraded:
	default:
		return nil, fmt.Errorf("verdict must be pass or degraded")
	}
	return s.resultRepo.Review(ctx, id, verdict, note, reviewer)
}

// --- execution ---

// RunPlanOnce executes one probe for the given plan ID synchronously, honoring
// the caller's context, and persists the result. The HTTP admin endpoint no
// longer uses it (it cannot outlive the request); it stays as the synchronous
// primitive for internal callers and tests.
func (s *PelicanProbeService) RunPlanOnce(ctx context.Context, planID int64) (*PelicanProbeResult, error) {
	row, err := s.planRepo.GetByID(ctx, planID)
	if err != nil {
		return nil, err
	}
	return s.runRow(ctx, row, true)
}

// StartPlanRun validates the plan and starts the probe in a goroutine that is
// detached from the caller's request lifecycle, then returns immediately.
//
// Why detached: a probe generates an SVG animation and legitimately runs for
// tens of seconds to minutes, while the frontend HTTP client aborts its request
// after 30s. Binding the probe to the request context made manual "run now"
// fail with `context canceled` although the work was still valid. The run now
// owns a 10 minute budget of its own and persists its result even when the
// requesting browser is long gone.
//
// Only cheap pre-checks happen inline (the plan exists and its test type is
// valid); the probe itself runs in the background. Nothing derived from the
// request-scoped *gin.Context may be touched inside the goroutine, so only the
// plain context.Context and the already-loaded plan row are captured.
func (s *PelicanProbeService) StartPlanRun(ctx context.Context, planID int64) (*PelicanProbeRunTrigger, error) {
	row, err := s.planRepo.GetByID(ctx, planID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrPelicanProbePlanNotFound
		}
		return nil, err
	}
	if _, err := normalizePelicanTestType(row.TestType); err != nil {
		return nil, err
	}
	if !s.runGuard.tryStart(planID) {
		return nil, fmt.Errorf("%w (plan_id=%d)", ErrPelicanProbeAlreadyRunning, planID)
	}

	// Values (logging/tracing) survive, cancellation and deadline do not.
	bgCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), pelicanProbeRunTimeout)
	go func() {
		defer cancel()
		defer s.runGuard.finish(planID)
		defer func() {
			if r := recover(); r != nil {
				logger.LegacyPrintf("service.pelican_probe", "[PelicanProbe] plan=%d panic: %v", planID, r)
			}
		}()
		if _, err := s.runRow(bgCtx, row, true); err != nil {
			logger.LegacyPrintf("service.pelican_probe", "[PelicanProbe] plan=%d run failed: %v", planID, err)
		}
	}()

	return &PelicanProbeRunTrigger{Started: true, PlanID: planID}, nil
}

// runRow executes the probe HTTP call and persists the result.
// When updateSchedule is true, last_run_at/next_run_at advance.
func (s *PelicanProbeService) runRow(ctx context.Context, row *PelicanProbePlanRow, updateSchedule bool) (*PelicanProbeResult, error) {
	startedAt := time.Now()
	rawHTML, promptTokens, completionTokens, latencyMs, runErr := s.executeProbe(ctx, row)
	finishedAt := time.Now()

	result := &PelicanProbeResult{
		PlanID:           row.ID,
		Status:           PelicanRunSuccess,
		RawHTML:          rawHTML,
		Verdict:          PelicanVerdictPending,
		LatencyMs:        latencyMs,
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		StartedAt:        startedAt,
		FinishedAt:       finishedAt,
	}
	if runErr != nil {
		result.Status = PelicanRunFailed
		result.ErrorMessage = runErr.Error()
	} else if row.TestType == PelicanTestTypeCandy {
		// Candy tests have an objective answer (21): ask the configured LLM
		// judge synchronously before persisting. A judge that cannot decide
		// leaves the verdict pending (the probe itself still succeeded), and
		// admins can always override the verdict via review.
		s.applyCandyJudge(ctx, row, result, rawHTML)
	}
	// Persist with a detached context: scheduled runs, manual runs and tests may
	// hand us a context that is already cancelled, but the probe result must
	// still land in the DB so a refresh shows it.
	dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), pelicanProbePersistenceTimeout)
	defer cancel()
	saved, err := s.resultRepo.Create(dbCtx, result)
	if err != nil {
		return nil, err
	}
	if err := s.resultRepo.PruneOldResults(dbCtx, row.ID, row.MaxResults); err != nil {
		return saved, err
	}
	if updateSchedule {
		nextRun, err := computeNextRun(row.CronExpression, time.Now())
		if err == nil {
			_ = s.planRepo.UpdateAfterRun(dbCtx, row.ID, time.Now(), nextRun)
		}
	}
	return saved, nil
}

// resolveProbeEndpoint decrypts the probe key and normalizes the base URL that
// both the probe call and the candy judge call must use.
func (s *PelicanProbeService) resolveProbeEndpoint(row *PelicanProbePlanRow) (baseURL, apiKey string, err error) {
	apiKey, err = s.encryptor.Decrypt(row.APIKeyEncrypted)
	if err != nil {
		return "", "", fmt.Errorf("decrypt probe api key: %w", err)
	}
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return "", "", fmt.Errorf("decrypt probe api key: empty key")
	}
	baseURL = strings.TrimSuffix(strings.TrimSpace(row.BaseURL), "/")
	if baseURL == "" {
		port := 8080
		if s.cfg != nil && s.cfg.Server.Port > 0 {
			port = s.cfg.Server.Port
		}
		baseURL = fmt.Sprintf("http://127.0.0.1:%d", port)
	}
	return baseURL, apiKey, nil
}

// executeProbe performs the OpenAI-compatible chat completion call.
//
// The probe streams (`stream: true`): a non-streaming request only yields
// response headers once the model has finished generating, which is exactly what
// made long generations die with `Client.Timeout exceeded while awaiting
// headers`. Streaming returns the first bytes immediately, so the run is bounded
// by its own (detached, 10 minute) context instead of by a header timeout.
// Transient upstream failures are retried with backoff inside that budget.
func (s *PelicanProbeService) executeProbe(ctx context.Context, row *PelicanProbePlanRow) (rawHTML string, promptTokens, completionTokens int, latencyMs int64, err error) {
	baseURL, apiKey, err := s.resolveProbeEndpoint(row)
	if err != nil {
		return "", 0, 0, 0, err
	}
	prompt := strings.TrimSpace(row.Prompt)
	if prompt == "" {
		prompt = DefaultPelicanPrompt
	}
	// Candy plans always run the fixed prompt, regardless of stored value.
	if row.TestType == PelicanTestTypeCandy {
		prompt = DefaultCandyPrompt
	}

	// Fixed sampling params keep runs longitudinally comparable.
	reqBody := map[string]any{
		"model":       row.ModelID,
		"stream":      true,
		"temperature": 0.7,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
	}
	if strings.TrimSpace(row.ReasoningEffort) != "" {
		reqBody["reasoning_effort"] = row.ReasoningEffort
	}
	bodyBytes, _ := json.Marshal(reqBody)

	start := time.Now()
	resp, err := s.doProbeChatCompletion(ctx, baseURL+"/v1/chat/completions", apiKey, bodyBytes)
	if err != nil {
		return "", 0, 0, time.Since(start).Milliseconds(), err
	}
	defer func() { _ = resp.Body.Close() }()

	content, promptTokens, completionTokens, truncated, err := readProbeChatStream(resp.Body)
	// Latency covers the whole generation (first byte through last chunk), which
	// is what the run duration means for the operator.
	latencyMs = time.Since(start).Milliseconds()
	if err != nil {
		return content, promptTokens, completionTokens, latencyMs, err
	}
	if truncated {
		return content, promptTokens, completionTokens, latencyMs,
			fmt.Errorf("upstream stream exceeded the %d byte probe limit", pelicanProbeMaxContentBytes)
	}
	if strings.TrimSpace(content) == "" {
		return "", promptTokens, completionTokens, latencyMs, fmt.Errorf("empty model content")
	}
	return stripCodeFences(content), promptTokens, completionTokens, latencyMs, nil
}

// stripCodeFences removes a single wrapping markdown code fence if present.
func stripCodeFences(s string) string {
	t := strings.TrimSpace(s)
	if !strings.HasPrefix(t, "```") {
		return t
	}
	// Drop opening fence line (``` or ```html).
	if idx := strings.Index(t, "\n"); idx >= 0 {
		t = t[idx+1:]
	} else {
		return t
	}
	if idx := strings.LastIndex(t, "```"); idx >= 0 {
		t = t[:idx]
	}
	return strings.TrimSpace(t)
}

// sanitizePlan strips secrets before returning plans to callers.
func sanitizePlan(row *PelicanProbePlanRow) *PelicanProbePlan {
	p := row.PelicanProbePlan
	p.HasAPIKey = strings.TrimSpace(row.APIKeyEncrypted) != ""
	// The judge key follows the same contract as the probe key: callers only
	// learn whether one is stored, never its value.
	p.HasJudgeAPIKey = strings.TrimSpace(row.JudgeAPIKeyEncrypted) != ""
	return &p
}
