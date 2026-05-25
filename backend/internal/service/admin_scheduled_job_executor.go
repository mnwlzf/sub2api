package service

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

type adminScheduledJobExecutor struct {
	backupService         *BackupService
	dataManagementService *DataManagementService
	channelMonitorService *ChannelMonitorService
	groupRepo             GroupRepository
	accountRepo           AccountRepository
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

func NewAdminScheduledJobExecutor(
	backupService *BackupService,
	dataManagementService *DataManagementService,
	channelMonitorService *ChannelMonitorService,
	groupRepo GroupRepository,
	accountRepo AccountRepository,
) AdminScheduledJobExecutor {
	return &adminScheduledJobExecutor{
		backupService:         backupService,
		dataManagementService: dataManagementService,
		channelMonitorService: channelMonitorService,
		groupRepo:             groupRepo,
		accountRepo:           accountRepo,
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
		"source_group_id":            payload.SourceGroupID,
		"target_group_ids":           syncedTargets,
		"source_account_count":       len(accountIDs),
		"synced_account_count":       len(filteredAccountIDs),
		"synced_target_group_count":  len(syncedTargets),
		"finished_at":                time.Now().UTC().Format(time.RFC3339),
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
