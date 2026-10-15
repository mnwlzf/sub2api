package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
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

type PelicanProbeService struct {
	planRepo    PelicanProbePlanRepository
	resultRepo  PelicanProbeResultRepository
	encryptor   SecretEncryptor
	settingRepo SettingRepository
	cfg         *config.Config
	httpClient  *http.Client
}

// NewPelicanProbeService creates a new PelicanProbeService.
func NewPelicanProbeService(
	planRepo PelicanProbePlanRepository,
	resultRepo PelicanProbeResultRepository,
	encryptor SecretEncryptor,
	settingRepo SettingRepository,
	cfg *config.Config,
) *PelicanProbeService {
	return &PelicanProbeService{
		planRepo:    planRepo,
		resultRepo:  resultRepo,
		encryptor:   encryptor,
		settingRepo: settingRepo,
		cfg:         cfg,
		httpClient:  &http.Client{Timeout: 5 * time.Minute},
	}
}

// --- plan CRUD ---

// CreatePlanInput carries plan creation fields (APIKey is plaintext, stored encrypted).
type CreatePlanInput struct {
	Name           string
	GroupID        int64
	ModelID        string
	BaseURL        string
	APIKey         string
	Prompt         string
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
	maxResults := in.MaxResults
	if maxResults <= 0 {
		maxResults = 50
	}
	encrypted, err := s.encryptor.Encrypt(strings.TrimSpace(in.APIKey))
	if err != nil {
		return nil, fmt.Errorf("encrypt api key: %w", err)
	}
	row := &PelicanProbePlanRow{
		PelicanProbePlan: PelicanProbePlan{
			Name:           strings.TrimSpace(in.Name),
			GroupID:        in.GroupID,
			ModelID:        strings.TrimSpace(in.ModelID),
			BaseURL:        strings.TrimSpace(in.BaseURL),
			CronExpression: cronExpr,
			Enabled:        in.Enabled,
			IsPublic:       in.IsPublic,
			MaxResults:     maxResults,
			NextRunAt:      &nextRun,
		},
		Prompt:          prompt,
		APIKeyEncrypted: encrypted,
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
	Name           *string
	ModelID        *string
	BaseURL        *string
	APIKey         string
	Prompt         *string
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
	if strings.TrimSpace(in.APIKey) != "" {
		encrypted, err := s.encryptor.Encrypt(strings.TrimSpace(in.APIKey))
		if err != nil {
			return nil, fmt.Errorf("encrypt api key: %w", err)
		}
		existing.APIKeyEncrypted = encrypted
	}
	if in.Prompt != nil && strings.TrimSpace(*in.Prompt) != "" {
		existing.Prompt = strings.TrimSpace(*in.Prompt)
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
		out.Plans = append(out.Plans, sanitizePlan(r))
	}
	results, err := s.resultRepo.ListPublicRecent(ctx, 60)
	if err != nil {
		return nil, err
	}
	out.Results = results
	return out, nil
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

// RunPlanOnce executes one probe for the given plan ID and persists the result.
func (s *PelicanProbeService) RunPlanOnce(ctx context.Context, planID int64) (*PelicanProbeResult, error) {
	row, err := s.planRepo.GetByID(ctx, planID)
	if err != nil {
		return nil, err
	}
	return s.runRow(ctx, row, true)
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
	}
	saved, err := s.resultRepo.Create(ctx, result)
	if err != nil {
		return nil, err
	}
	if err := s.resultRepo.PruneOldResults(ctx, row.ID, row.MaxResults); err != nil {
		return saved, err
	}
	if updateSchedule {
		nextRun, err := computeNextRun(row.CronExpression, time.Now())
		if err == nil {
			_ = s.planRepo.UpdateAfterRun(ctx, row.ID, time.Now(), nextRun)
		}
	}
	return saved, nil
}

// executeProbe performs the OpenAI-compatible chat completion call.
func (s *PelicanProbeService) executeProbe(ctx context.Context, row *PelicanProbePlanRow) (rawHTML string, promptTokens, completionTokens int, latencyMs int64, err error) {
	apiKey, err := s.encryptor.Decrypt(row.APIKeyEncrypted)
	if err != nil || strings.TrimSpace(apiKey) == "" {
		return "", 0, 0, 0, fmt.Errorf("decrypt probe api key: %w", err)
	}
	baseURL := strings.TrimSuffix(strings.TrimSpace(row.BaseURL), "/")
	if baseURL == "" {
		port := 8080
		if s.cfg != nil && s.cfg.Server.Port > 0 {
			port = s.cfg.Server.Port
		}
		baseURL = fmt.Sprintf("http://127.0.0.1:%d", port)
	}
	prompt := strings.TrimSpace(row.Prompt)
	if prompt == "" {
		prompt = DefaultPelicanPrompt
	}

	// Fixed sampling params keep runs longitudinally comparable.
	reqBody := map[string]any{
		"model":       row.ModelID,
		"stream":      false,
		"temperature": 0.7,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
	}
	bodyBytes, _ := json.Marshal(reqBody)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/v1/chat/completions", bytes.NewReader(bodyBytes))
	if err != nil {
		return "", 0, 0, 0, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+strings.TrimSpace(apiKey))

	start := time.Now()
	resp, err := s.httpClient.Do(httpReq)
	latencyMs = time.Since(start).Milliseconds()
	if err != nil {
		return "", 0, 0, latencyMs, err
	}
	defer func() { _ = resp.Body.Close() }()
	respBytes, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", 0, 0, latencyMs, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet := strings.TrimSpace(string(respBytes))
		if len(snippet) > 500 {
			snippet = snippet[:500]
		}
		return "", 0, 0, latencyMs, fmt.Errorf("upstream HTTP %d: %s", resp.StatusCode, snippet)
	}
	content := gjson.GetBytes(respBytes, "choices.0.message.content").String()
	promptTokens = int(gjson.GetBytes(respBytes, "usage.prompt_tokens").Int())
	completionTokens = int(gjson.GetBytes(respBytes, "usage.completion_tokens").Int())
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
	return &p
}
