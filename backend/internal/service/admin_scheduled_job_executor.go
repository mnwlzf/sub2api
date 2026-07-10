package service

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
)

const (
	relayCodexGroupRatioGroup = "codex"
	relayPricingURL           = "https://relayai.tech/api/pricing"
	relayCodexYunjinKeyword   = "云锦"
)

type adminScheduledJobExecutor struct {
	backupService         *BackupService
	dataManagementService *DataManagementService
	channelMonitorService *ChannelMonitorService
	groupRepo             GroupRepository
	accountRepo           AccountRepository
	pricingRemoteClient   PricingRemoteClient
}

type adminScheduledBackupPayload struct {
	ExpireDays int `json:"expire_days"`
}

type adminScheduledDataManagementPayload struct {
	UploadToS3  bool   `json:"upload_to_s3"`
	S3ProfileID string `json:"s3_profile_id"`
	PostgresID  string `json:"postgres_profile_id"`
	RedisID     string `json:"redis_profile_id"`
}

type adminScheduledSyncCodexFreeGroupsPayload struct {
	SourceGroupID  int64   `json:"source_group_id"`
	TargetGroupIDs []int64 `json:"target_group_ids"`
}

type adminScheduledOpenAIOAuthModelMappingPayload struct {
	ModelMapping map[string]string `json:"model_mapping"`
}

type adminScheduledRelayCodexGroupRatioPayload struct {
	AccountNameContains string                                `json:"account_name_contains"`
	RatioMappingRules   []adminScheduledRelayCodexMappingRule `json:"ratio_mapping_rules"`
}

type adminScheduledRelayCodexMappingRule struct {
	Ratio        float64           `json:"ratio"`
	TargetModel  string            `json:"target_model"`
	ModelMapping map[string]string `json:"model_mapping"`
}

type openAIOAuthModelMappingUpdater interface {
	UpdateOpenAIOAuthModelMapping(ctx context.Context, mapping map[string]string) (matched int64, updatedIDs []int64, err error)
}

type namedOpenAIOAuthModelMappingUpdater interface {
	UpdateOpenAIOAuthModelMappingByNameContains(ctx context.Context, nameContains string, mapping map[string]string) (matched int64, updatedIDs []int64, err error)
}

type sharedOpenAIOAuthModelMappingUpdater interface {
	UpdateSharedOpenAIOAuthModelMapping(ctx context.Context, mapping map[string]string) (matched int64, updatedIDs []int64, err error)
}

type exclusiveOpenAIOAuthModelMappingUpdater interface {
	UpdateExclusiveOpenAIOAuthModelMapping(ctx context.Context, mapping map[string]string) (matched int64, updatedIDs []int64, err error)
}

func NewAdminScheduledJobExecutor(
	backupService *BackupService,
	dataManagementService *DataManagementService,
	channelMonitorService *ChannelMonitorService,
	groupRepo GroupRepository,
	accountRepo AccountRepository,
	pricingRemoteClient PricingRemoteClient,
) AdminScheduledJobExecutor {
	return &adminScheduledJobExecutor{
		backupService:         backupService,
		dataManagementService: dataManagementService,
		channelMonitorService: channelMonitorService,
		groupRepo:             groupRepo,
		accountRepo:           accountRepo,
		pricingRemoteClient:   pricingRemoteClient,
	}
}

func (e *adminScheduledJobExecutor) Execute(ctx context.Context, job *AdminScheduledJob) (string, string, error) {
	switch job.JobType {
	case AdminScheduledJobTypeBackup:
		return e.executeBackup(ctx, job)
	case AdminScheduledJobTypeDataManagementFull:
		return e.executeDataManagementFull(ctx, job)
	case AdminScheduledJobTypeChannelMonitorMaint:
		return e.executeChannelMonitorMaintenance(ctx)
	case AdminScheduledJobTypeSyncCodexFreeGroups:
		return e.executeSyncCodexFreeGroups(ctx, job)
	case AdminScheduledJobTypeCleanupErrorAccounts:
		return e.executeCleanupErrorAccounts(ctx)
	case AdminScheduledJobTypeUpdateOpenAIOAuthModelMapping:
		return e.executeUpdateOpenAIOAuthModelMapping(ctx, job)
	case AdminScheduledJobTypeUpdateOpenAIOAuthSharedModelMapping:
		return e.executeUpdateSharedOpenAIOAuthModelMapping(ctx, job)
	case AdminScheduledJobTypeUpdateOpenAIOAuthExclusiveModelMapping:
		return e.executeUpdateExclusiveOpenAIOAuthModelMapping(ctx, job)
	case AdminScheduledJobTypeTrackRelayCodexGroupRatio:
		return e.executeTrackRelayCodexGroupRatio(ctx, job)
	default:
		return "", "", fmt.Errorf("unsupported job type: %s", job.JobType)
	}
}

func (e *adminScheduledJobExecutor) executeBackup(ctx context.Context, job *AdminScheduledJob) (string, string, error) {
	if e.backupService == nil {
		return "", "", fmt.Errorf("backup service unavailable")
	}
	payload := adminScheduledBackupPayload{}
	_ = json.Unmarshal([]byte(job.PayloadJSON), &payload)
	record, err := e.backupService.StartBackup(ctx, "scheduled_job", payload.ExpireDays)
	if err != nil {
		return "", "", err
	}
	buf, _ := json.Marshal(record)
	return fmt.Sprintf("backup started: %s", record.ID), string(buf), nil
}

func (e *adminScheduledJobExecutor) executeDataManagementFull(ctx context.Context, job *AdminScheduledJob) (string, string, error) {
	if e.dataManagementService == nil {
		return "", "", fmt.Errorf("data management service unavailable")
	}
	payload := adminScheduledDataManagementPayload{}
	_ = json.Unmarshal([]byte(job.PayloadJSON), &payload)
	input := DataManagementCreateBackupJobInput{
		BackupType:  "full",
		UploadToS3:  payload.UploadToS3,
		TriggeredBy: "scheduled_job",
		S3ProfileID: payload.S3ProfileID,
		PostgresID:  payload.PostgresID,
		RedisID:     payload.RedisID,
	}
	_ = ctx
	_ = input
	return "", "", fmt.Errorf("data management full backup is currently unavailable")
}

func (e *adminScheduledJobExecutor) executeChannelMonitorMaintenance(ctx context.Context) (string, string, error) {
	if e.channelMonitorService == nil {
		return "", "", fmt.Errorf("channel monitor service unavailable")
	}
	startedAt := time.Now().UTC()
	if err := e.channelMonitorService.RunDailyMaintenance(ctx); err != nil {
		return "", "", err
	}
	result, _ := json.Marshal(map[string]any{
		"started_at":  startedAt.Format(time.RFC3339),
		"finished_at": time.Now().UTC().Format(time.RFC3339),
	})
	return "channel monitor maintenance completed", string(result), nil
}

func (e *adminScheduledJobExecutor) executeSyncCodexFreeGroups(ctx context.Context, job *AdminScheduledJob) (string, string, error) {
	if e.groupRepo == nil {
		return "", "", fmt.Errorf("group repository unavailable")
	}
	payload := adminScheduledSyncCodexFreeGroupsPayload{}
	if err := json.Unmarshal([]byte(job.PayloadJSON), &payload); err != nil {
		return "", "", fmt.Errorf("invalid payload_json: %w", err)
	}
	if payload.SourceGroupID <= 0 {
		return "", "", fmt.Errorf("source_group_id is required")
	}
	if len(payload.TargetGroupIDs) == 0 {
		return "", "", fmt.Errorf("target_group_ids is required")
	}

	sourceGroup, err := e.groupRepo.GetByIDLite(ctx, payload.SourceGroupID)
	if err != nil {
		return "", "", fmt.Errorf("source group not found: %w", err)
	}

	targetGroupIDs := make([]int64, 0, len(payload.TargetGroupIDs))
	seen := make(map[int64]struct{}, len(payload.TargetGroupIDs))
	for _, targetGroupID := range payload.TargetGroupIDs {
		if targetGroupID <= 0 || targetGroupID == payload.SourceGroupID {
			return "", "", fmt.Errorf("invalid target group id: %d", targetGroupID)
		}
		if _, exists := seen[targetGroupID]; exists {
			continue
		}
		seen[targetGroupID] = struct{}{}
		targetGroup, targetErr := e.groupRepo.GetByIDLite(ctx, targetGroupID)
		if targetErr != nil {
			return "", "", fmt.Errorf("target group %d not found: %w", targetGroupID, targetErr)
		}
		if targetGroup.Platform != sourceGroup.Platform {
			return "", "", fmt.Errorf("target group %d platform mismatch: expected %s, got %s", targetGroupID, sourceGroup.Platform, targetGroup.Platform)
		}
		targetGroupIDs = append(targetGroupIDs, targetGroupID)
	}

	accountIDs, err := e.groupRepo.GetAccountIDsByGroupIDs(ctx, []int64{payload.SourceGroupID})
	if err != nil {
		return "", "", fmt.Errorf("failed to load source group accounts: %w", err)
	}
	filteredAccountIDs, err := e.filterOAuthOnlyAccounts(ctx, sourceGroup, accountIDs)
	if err != nil {
		return "", "", err
	}

	syncedTargets := make([]int64, 0, len(targetGroupIDs))
	for _, targetGroupID := range targetGroupIDs {
		targetGroup, targetErr := e.groupRepo.GetByIDLite(ctx, targetGroupID)
		if targetErr != nil {
			return "", "", fmt.Errorf("target group %d not found during sync: %w", targetGroupID, targetErr)
		}
		targetAccountIDs, filterErr := e.filterOAuthOnlyAccounts(ctx, targetGroup, filteredAccountIDs)
		if filterErr != nil {
			return "", "", filterErr
		}
		if _, clearErr := e.groupRepo.DeleteAccountGroupsByGroupID(ctx, targetGroupID); clearErr != nil {
			return "", "", fmt.Errorf("failed to clear target group %d bindings: %w", targetGroupID, clearErr)
		}
		if bindErr := e.groupRepo.BindAccountsToGroup(ctx, targetGroupID, targetAccountIDs); bindErr != nil {
			return "", "", fmt.Errorf("failed to bind target group %d accounts: %w", targetGroupID, bindErr)
		}
		syncedTargets = append(syncedTargets, targetGroupID)
	}

	result, _ := json.Marshal(map[string]any{
		"source_group_id":           payload.SourceGroupID,
		"target_group_ids":          syncedTargets,
		"source_account_count":      len(accountIDs),
		"synced_account_count":      len(filteredAccountIDs),
		"synced_target_group_count": len(syncedTargets),
		"finished_at":               time.Now().UTC().Format(time.RFC3339),
	})
	return fmt.Sprintf("synced %d accounts from group %d to %d groups", len(filteredAccountIDs), payload.SourceGroupID, len(syncedTargets)), string(result), nil
}

func (e *adminScheduledJobExecutor) filterOAuthOnlyAccounts(ctx context.Context, group *Group, accountIDs []int64) ([]int64, error) {
	if group == nil || !group.RequireOAuthOnly || len(accountIDs) == 0 {
		return accountIDs, nil
	}
	if e.accountRepo == nil {
		return nil, fmt.Errorf("account repository unavailable")
	}
	accounts, err := e.accountRepo.GetByIDs(ctx, accountIDs)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch accounts for oauth filter: %w", err)
	}
	oauthIDs := make(map[int64]struct{}, len(accounts))
	for _, acc := range accounts {
		if acc != nil && acc.Type != AccountTypeAPIKey {
			oauthIDs[acc.ID] = struct{}{}
		}
	}
	filtered := make([]int64, 0, len(accountIDs))
	for _, accountID := range accountIDs {
		if _, ok := oauthIDs[accountID]; ok {
			filtered = append(filtered, accountID)
		}
	}
	return filtered, nil
}

func (e *adminScheduledJobExecutor) executeCleanupErrorAccounts(ctx context.Context) (string, string, error) {
	if e.accountRepo == nil {
		return "", "", fmt.Errorf("account repository unavailable")
	}

	params := pagination.DefaultPagination()
	params.Page = 1
	params.PageSize = 200
	params.SortBy = "id"
	params.SortOrder = pagination.SortOrderAsc

	var totalMatched int64
	var deleted int
	var skipped int

	for {
		accounts, page, err := e.accountRepo.ListWithFilters(ctx, params, "", "", StatusError, "", 0, "")
		if err != nil {
			return "", "", fmt.Errorf("list error accounts: %w", err)
		}
		if page != nil && totalMatched == 0 {
			totalMatched = page.Total
		}
		if len(accounts) == 0 {
			break
		}

		for i := range accounts {
			if err := e.accountRepo.Delete(ctx, accounts[i].ID); err != nil {
				skipped++
				continue
			}
			deleted++
		}

		// Always continue from page 1 because records are being deleted continuously.
		params.Page = 1
	}

	result, _ := json.Marshal(map[string]any{
		"matched":     totalMatched,
		"deleted":     deleted,
		"skipped":     skipped,
		"finished_at": time.Now().UTC().Format(time.RFC3339),
	})
	return fmt.Sprintf("deleted %d error accounts", deleted), string(result), nil
}

type relayPricingResponse struct {
	GroupRatio map[string]float64 `json:"group_ratio"`
}

func (e *adminScheduledJobExecutor) executeTrackRelayCodexGroupRatio(ctx context.Context, job *AdminScheduledJob) (string, string, error) {
	if e.pricingRemoteClient == nil {
		return "", "", fmt.Errorf("pricing remote client unavailable")
	}

	body, err := e.pricingRemoteClient.FetchPricingJSON(ctx, relayPricingURL)
	if err != nil {
		return "", "", fmt.Errorf("fetch relay pricing: %w", err)
	}

	var pricingPayload relayPricingResponse
	if err := json.Unmarshal(body, &pricingPayload); err != nil {
		return "", "", fmt.Errorf("parse relay pricing: %w", err)
	}
	ratio, ok := pricingPayload.GroupRatio[relayCodexGroupRatioGroup]
	if !ok {
		return "", "", fmt.Errorf("group_ratio.%s not found", relayCodexGroupRatioGroup)
	}

	fetchedAt := time.Now().UTC()
	result := map[string]any{
		"group":       relayCodexGroupRatioGroup,
		"codex_ratio": ratio,
		"source_url":  relayPricingURL,
		"fetched_at":  fetchedAt.Format(time.RFC3339),
	}

	payload, err := parseScheduledRelayCodexGroupRatioPayload(job.PayloadJSON)
	if err != nil {
		return "", "", err
	}
	rule, matchedRule := selectRelayCodexRatioMappingRule(payload.RatioMappingRules, ratio)
	if !matchedRule {
		result["mapping_skipped_reason"] = "no matching ratio rule"
		resultJSON, _ := json.Marshal(result)
		return fmt.Sprintf("relay %s group ratio %.4f; no mapping rule matched", relayCodexGroupRatioGroup, ratio), string(resultJSON), nil
	}

	updater, ok := e.accountRepo.(namedOpenAIOAuthModelMappingUpdater)
	if !ok || updater == nil {
		return "", "", fmt.Errorf("account repository does not support named openai oauth model mapping update")
	}
	mapping := relayCodexRuleModelMapping(rule)
	if len(mapping) == 0 {
		return "", "", fmt.Errorf("matched ratio rule has empty model_mapping")
	}
	matched, updatedIDs, err := updater.UpdateOpenAIOAuthModelMappingByNameContains(ctx, payload.AccountNameContains, mapping)
	if err != nil {
		return "", "", err
	}
	result["account_name_contains"] = payload.AccountNameContains
	result["matched_ratio"] = rule.Ratio
	result["matched"] = matched
	result["updated"] = len(updatedIDs)
	result["updated_ids"] = updatedIDs
	result["model_mapping"] = mapping
	resultJSON, _ := json.Marshal(result)
	return fmt.Sprintf("relay %s group ratio %.4f; updated %d yunjin openai oauth accounts", relayCodexGroupRatioGroup, ratio, len(updatedIDs)), string(resultJSON), nil
}

func (e *adminScheduledJobExecutor) executeUpdateOpenAIOAuthModelMapping(ctx context.Context, job *AdminScheduledJob) (string, string, error) {
	updater, ok := e.accountRepo.(openAIOAuthModelMappingUpdater)
	if !ok || updater == nil {
		return "", "", fmt.Errorf("account repository does not support openai oauth model mapping update")
	}

	payload := adminScheduledOpenAIOAuthModelMappingPayload{}
	if err := json.Unmarshal([]byte(job.PayloadJSON), &payload); err != nil {
		return "", "", fmt.Errorf("invalid payload_json: %w", err)
	}
	modelMapping := normalizeModelMapping(payload.ModelMapping)
	if len(modelMapping) == 0 {
		return "", "", fmt.Errorf("model_mapping is required")
	}

	matched, updatedIDs, err := updater.UpdateOpenAIOAuthModelMapping(ctx, modelMapping)
	if err != nil {
		return "", "", err
	}
	result, _ := json.Marshal(map[string]any{
		"matched":       matched,
		"updated":       len(updatedIDs),
		"updated_ids":   updatedIDs,
		"model_mapping": modelMapping,
		"finished_at":   time.Now().UTC().Format(time.RFC3339),
	})
	return fmt.Sprintf("updated %d openai oauth accounts", len(updatedIDs)), string(result), nil
}

func (e *adminScheduledJobExecutor) executeUpdateSharedOpenAIOAuthModelMapping(ctx context.Context, job *AdminScheduledJob) (string, string, error) {
	updater, ok := e.accountRepo.(sharedOpenAIOAuthModelMappingUpdater)
	if !ok || updater == nil {
		return "", "", fmt.Errorf("account repository does not support shared openai oauth model mapping update")
	}

	modelMapping, err := parseScheduledOpenAIOAuthModelMapping(job.PayloadJSON)
	if err != nil {
		return "", "", err
	}
	matched, updatedIDs, err := updater.UpdateSharedOpenAIOAuthModelMapping(ctx, modelMapping)
	if err != nil {
		return "", "", err
	}
	result, _ := json.Marshal(map[string]any{
		"matched":         matched,
		"updated":         len(updatedIDs),
		"updated_ids":     updatedIDs,
		"group_ids":       []int64{2, 5, 11},
		"excluded_groups": []int64{12},
		"model_mapping":   modelMapping,
		"finished_at":     time.Now().UTC().Format(time.RFC3339),
	})
	return fmt.Sprintf("updated %d shared openai oauth accounts", len(updatedIDs)), string(result), nil
}

func (e *adminScheduledJobExecutor) executeUpdateExclusiveOpenAIOAuthModelMapping(ctx context.Context, job *AdminScheduledJob) (string, string, error) {
	updater, ok := e.accountRepo.(exclusiveOpenAIOAuthModelMappingUpdater)
	if !ok || updater == nil {
		return "", "", fmt.Errorf("account repository does not support exclusive openai oauth model mapping update")
	}

	modelMapping, err := parseScheduledOpenAIOAuthModelMapping(job.PayloadJSON)
	if err != nil {
		return "", "", err
	}
	matched, updatedIDs, err := updater.UpdateExclusiveOpenAIOAuthModelMapping(ctx, modelMapping)
	if err != nil {
		return "", "", err
	}
	result, _ := json.Marshal(map[string]any{
		"matched":       matched,
		"updated":       len(updatedIDs),
		"updated_ids":   updatedIDs,
		"group_ids":     []int64{12},
		"model_mapping": modelMapping,
		"finished_at":   time.Now().UTC().Format(time.RFC3339),
	})
	return fmt.Sprintf("updated %d exclusive openai oauth accounts", len(updatedIDs)), string(result), nil
}

func parseScheduledOpenAIOAuthModelMapping(payloadJSON string) (map[string]string, error) {
	payload := adminScheduledOpenAIOAuthModelMappingPayload{}
	if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
		return nil, fmt.Errorf("invalid payload_json: %w", err)
	}
	modelMapping := normalizeModelMapping(payload.ModelMapping)
	if len(modelMapping) == 0 {
		return nil, fmt.Errorf("model_mapping is required")
	}
	return modelMapping, nil
}

func parseScheduledRelayCodexGroupRatioPayload(payloadJSON string) (adminScheduledRelayCodexGroupRatioPayload, error) {
	payload := adminScheduledRelayCodexGroupRatioPayload{}
	normalized := normalizeAdminScheduledJobPayload(payloadJSON)
	if err := json.Unmarshal([]byte(normalized), &payload); err != nil {
		return payload, fmt.Errorf("invalid payload_json: %w", err)
	}
	payload.AccountNameContains = strings.TrimSpace(payload.AccountNameContains)
	if payload.AccountNameContains == "" {
		payload.AccountNameContains = relayCodexYunjinKeyword
	}
	if len(payload.RatioMappingRules) == 0 {
		payload.RatioMappingRules = defaultRelayCodexRatioMappingRules()
	}
	for i := range payload.RatioMappingRules {
		payload.RatioMappingRules[i].TargetModel = strings.TrimSpace(payload.RatioMappingRules[i].TargetModel)
		payload.RatioMappingRules[i].ModelMapping = normalizeModelMapping(payload.RatioMappingRules[i].ModelMapping)
	}
	return payload, nil
}

func defaultRelayCodexRatioMappingRules() []adminScheduledRelayCodexMappingRule {
	return []adminScheduledRelayCodexMappingRule{
		{Ratio: 0.06, TargetModel: "gpt-5.4-mini"},
		{Ratio: 0.07, TargetModel: "gpt-5.4"},
		{Ratio: 0.08, TargetModel: "gpt-5.5"},
	}
}

func selectRelayCodexRatioMappingRule(rules []adminScheduledRelayCodexMappingRule, ratio float64) (adminScheduledRelayCodexMappingRule, bool) {
	normalizedRatio := roundRelayCodexRatio(ratio)
	for _, rule := range rules {
		if math.Abs(roundRelayCodexRatio(rule.Ratio)-normalizedRatio) <= 0.000001 {
			return rule, true
		}
	}
	return adminScheduledRelayCodexMappingRule{}, false
}

func roundRelayCodexRatio(ratio float64) float64 {
	return math.Round(ratio*10000) / 10000
}

func relayCodexRuleModelMapping(rule adminScheduledRelayCodexMappingRule) map[string]string {
	mapping := normalizeModelMapping(rule.ModelMapping)
	if len(mapping) > 0 {
		return mapping
	}
	target := strings.TrimSpace(rule.TargetModel)
	if target == "" {
		return nil
	}
	return map[string]string{
		"gpt-5.3-codex": target,
		"gpt-5.4":       target,
		"gpt-5.4-mini":  target,
		"gpt-5.5":       target,
	}
}

func normalizeModelMapping(input map[string]string) map[string]string {
	out := make(map[string]string, len(input))
	for source, target := range input {
		source = strings.TrimSpace(source)
		target = strings.TrimSpace(target)
		if source == "" || target == "" {
			continue
		}
		out[source] = target
	}
	return out
}
