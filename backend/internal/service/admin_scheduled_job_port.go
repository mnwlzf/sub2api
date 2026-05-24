package service

import (
	"context"
	"time"
)

const (
	AdminScheduledJobTypeBackup              = "backup_postgres"
	AdminScheduledJobTypeDataManagementFull  = "data_management_full_backup"
	AdminScheduledJobTypeChannelMonitorMaint = "channel_monitor_maintenance"
	AdminScheduledJobTypeSyncCodexFreeGroups = "sync_codex_free_group_accounts"

	AdminScheduledJobTriggerManual    = "manual"
	AdminScheduledJobTriggerScheduled = "scheduled"

	AdminScheduledJobStatusRunning = "running"
	AdminScheduledJobStatusSuccess = "success"
	AdminScheduledJobStatusFailed  = "failed"
)

type AdminScheduledJob struct {
	ID             int64      `json:"id"`
	Name           string     `json:"name"`
	JobType        string     `json:"job_type"`
	CronExpression string     `json:"cron_expression"`
	Enabled        bool       `json:"enabled"`
	PayloadJSON    string     `json:"payload_json"`
	RetentionLimit int        `json:"retention_limit"`
	LastRunAt      *time.Time `json:"last_run_at"`
	NextRunAt      *time.Time `json:"next_run_at"`
	LastStatus     string     `json:"last_status"`
	LastMessage    string     `json:"last_message"`
	CreatedBy      int64      `json:"created_by"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

type AdminScheduledJobRun struct {
	ID              int64      `json:"id"`
	JobID           int64      `json:"job_id"`
	TriggerType     string     `json:"trigger_type"`
	Status          string     `json:"status"`
	Message         string     `json:"message"`
	ResultJSON      string     `json:"result_json"`
	StartedAt       time.Time  `json:"started_at"`
	FinishedAt      *time.Time `json:"finished_at"`
	CreatedAt       time.Time  `json:"created_at"`
	TriggeredByUser *int64     `json:"triggered_by_user"`
}

type AdminScheduledJobCreateParams struct {
	Name           string
	JobType        string
	CronExpression string
	Enabled        bool
	PayloadJSON    string
	RetentionLimit int
	CreatedBy      int64
}

type AdminScheduledJobUpdateParams struct {
	Name           *string
	CronExpression *string
	Enabled        *bool
	PayloadJSON    *string
	RetentionLimit *int
}

type AdminScheduledJobRunRequest struct {
	TriggeredByUser *int64
	TriggerType     string
}

type AdminScheduledJobExecutor interface {
	Execute(ctx context.Context, job *AdminScheduledJob) (message string, resultJSON string, err error)
}

type AdminScheduledJobRepository interface {
	Create(ctx context.Context, job *AdminScheduledJob) (*AdminScheduledJob, error)
	GetByID(ctx context.Context, id int64) (*AdminScheduledJob, error)
	List(ctx context.Context) ([]*AdminScheduledJob, error)
	ListDue(ctx context.Context, now time.Time) ([]*AdminScheduledJob, error)
	Update(ctx context.Context, job *AdminScheduledJob) (*AdminScheduledJob, error)
	Delete(ctx context.Context, id int64) error
	UpdateAfterRun(ctx context.Context, id int64, lastRunAt time.Time, nextRunAt time.Time, status, message string) error
}

type AdminScheduledJobRunRepository interface {
	Create(ctx context.Context, run *AdminScheduledJobRun) (*AdminScheduledJobRun, error)
	UpdateFinished(ctx context.Context, runID int64, status, message, resultJSON string, finishedAt time.Time) error
	ListByJobID(ctx context.Context, jobID int64, limit int) ([]*AdminScheduledJobRun, error)
	PruneOldRuns(ctx context.Context, jobID int64, keepCount int) error
}
