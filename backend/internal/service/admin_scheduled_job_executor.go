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

func NewAdminScheduledJobExecutor(
	backupService *BackupService,
	dataManagementService *DataManagementService,
	channelMonitorService *ChannelMonitorService,
) AdminScheduledJobExecutor {
	return &adminScheduledJobExecutor{
		backupService:         backupService,
		dataManagementService: dataManagementService,
		channelMonitorService: channelMonitorService,
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
