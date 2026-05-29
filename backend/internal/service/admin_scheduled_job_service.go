package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

var adminScheduledJobCronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

type AdminScheduledJobService struct {
	jobRepo  AdminScheduledJobRepository
	runRepo  AdminScheduledJobRunRepository
	executor AdminScheduledJobExecutor
	groupRepo GroupRepository
}

func NewAdminScheduledJobService(
	jobRepo AdminScheduledJobRepository,
	runRepo AdminScheduledJobRunRepository,
	executor AdminScheduledJobExecutor,
	groupRepo GroupRepository,
) *AdminScheduledJobService {
	return &AdminScheduledJobService{
		jobRepo:  jobRepo,
		runRepo:  runRepo,
		executor: executor,
		groupRepo: groupRepo,
	}
}

func (s *AdminScheduledJobService) List(ctx context.Context) ([]*AdminScheduledJob, error) {
	return s.jobRepo.List(ctx)
}

func (s *AdminScheduledJobService) Get(ctx context.Context, id int64) (*AdminScheduledJob, error) {
	return s.jobRepo.GetByID(ctx, id)
}

func (s *AdminScheduledJobService) Create(ctx context.Context, p AdminScheduledJobCreateParams) (*AdminScheduledJob, error) {
	if err := validateAdminScheduledJob(p.JobType, p.CronExpression, p.PayloadJSON, p.RetentionLimit); err != nil {
		return nil, err
	}
	if err := s.validateGroupReferences(ctx, p.JobType, p.PayloadJSON); err != nil {
		return nil, err
	}
	nextRun, err := computeAdminScheduledJobNextRun(p.CronExpression, time.Now())
	if err != nil {
		return nil, err
	}
	job := &AdminScheduledJob{
		Name:           strings.TrimSpace(p.Name),
		JobType:        p.JobType,
		CronExpression: strings.TrimSpace(p.CronExpression),
		Enabled:        p.Enabled,
		PayloadJSON:    normalizeAdminScheduledJobPayload(p.PayloadJSON),
		RetentionLimit: normalizeAdminScheduledJobRetention(p.RetentionLimit),
		CreatedBy:      p.CreatedBy,
	}
	if job.Enabled {
		job.NextRunAt = &nextRun
	}
	return s.jobRepo.Create(ctx, job)
}

func (s *AdminScheduledJobService) Update(ctx context.Context, id int64, p AdminScheduledJobUpdateParams) (*AdminScheduledJob, error) {
	job, err := s.jobRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if p.Name != nil {
		job.Name = strings.TrimSpace(*p.Name)
	}
	if p.CronExpression != nil {
		job.CronExpression = strings.TrimSpace(*p.CronExpression)
	}
	if p.Enabled != nil {
		job.Enabled = *p.Enabled
	}
	if p.PayloadJSON != nil {
		job.PayloadJSON = normalizeAdminScheduledJobPayload(*p.PayloadJSON)
	}
	if p.RetentionLimit != nil {
		job.RetentionLimit = normalizeAdminScheduledJobRetention(*p.RetentionLimit)
	}
	if err := validateAdminScheduledJob(job.JobType, job.CronExpression, job.PayloadJSON, job.RetentionLimit); err != nil {
		return nil, err
	}
	if err := s.validateGroupReferences(ctx, job.JobType, job.PayloadJSON); err != nil {
		return nil, err
	}
	if job.Enabled {
		nextRun, err := computeAdminScheduledJobNextRun(job.CronExpression, time.Now())
		if err != nil {
			return nil, err
		}
		job.NextRunAt = &nextRun
	} else {
		job.NextRunAt = nil
	}
	return s.jobRepo.Update(ctx, job)
}

func (s *AdminScheduledJobService) validateGroupReferences(ctx context.Context, jobType, payloadJSON string) error {
	jobType = strings.TrimSpace(jobType)
	if s.groupRepo == nil {
		return nil
	}
	if jobType == AdminScheduledJobTypeSyncCodexFreeGroups {
		var payload adminScheduledSyncCodexFreeGroupsPayload
		if err := json.Unmarshal([]byte(normalizeAdminScheduledJobPayload(payloadJSON)), &payload); err != nil {
			return fmt.Errorf("invalid payload_json: %w", err)
		}
		if _, err := s.groupRepo.GetByIDLite(ctx, payload.SourceGroupID); err != nil {
			return fmt.Errorf("source group %d not found: %w", payload.SourceGroupID, err)
		}
		for _, groupID := range payload.TargetGroupIDs {
			if _, err := s.groupRepo.GetByIDLite(ctx, groupID); err != nil {
				return fmt.Errorf("target group %d not found: %w", groupID, err)
			}
		}
		return nil
	}
	if jobType == AdminScheduledJobTypeUpdateOpenAIOAuthSharedModelMapping || jobType == AdminScheduledJobTypeUpdateOpenAIOAuthExclusiveModelMapping {
		for _, groupID := range adminScheduledOpenAIOAuthModelMappingGroupIDs(jobType) {
			if _, err := s.groupRepo.GetByIDLite(ctx, groupID); err != nil {
				return fmt.Errorf("group %d not found: %w", groupID, err)
			}
		}
		return nil
	}
	return nil
}

func adminScheduledOpenAIOAuthModelMappingGroupIDs(jobType string) []int64 {
	switch strings.TrimSpace(jobType) {
	case AdminScheduledJobTypeUpdateOpenAIOAuthSharedModelMapping:
		return []int64{2, 5, 11}
	case AdminScheduledJobTypeUpdateOpenAIOAuthExclusiveModelMapping:
		return []int64{12}
	default:
		return nil
	}
}

func (s *AdminScheduledJobService) Delete(ctx context.Context, id int64) error {
	return s.jobRepo.Delete(ctx, id)
}

func (s *AdminScheduledJobService) ListRuns(ctx context.Context, jobID int64, limit int) ([]*AdminScheduledJobRun, error) {
	if limit <= 0 {
		limit = 50
	}
	return s.runRepo.ListByJobID(ctx, jobID, limit)
}

func (s *AdminScheduledJobService) RunNow(ctx context.Context, id int64, req AdminScheduledJobRunRequest) (*AdminScheduledJobRun, error) {
	job, err := s.jobRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	run, err := s.createRun(ctx, job, req)
	if err != nil {
		return nil, err
	}
	s.executeRun(ctx, job, run, req.TriggerType)
	return run, nil
}

func (s *AdminScheduledJobService) RunDueJobs(ctx context.Context, now time.Time) {
	jobs, err := s.jobRepo.ListDue(ctx, now)
	if err != nil {
		return
	}
	for _, job := range jobs {
		run, runErr := s.createRun(ctx, job, AdminScheduledJobRunRequest{
			TriggerType: AdminScheduledJobTriggerScheduled,
		})
		if runErr != nil {
			continue
		}
		s.executeRun(ctx, job, run, AdminScheduledJobTriggerScheduled)
	}
}

func (s *AdminScheduledJobService) createRun(ctx context.Context, job *AdminScheduledJob, req AdminScheduledJobRunRequest) (*AdminScheduledJobRun, error) {
	triggerType := strings.TrimSpace(req.TriggerType)
	if triggerType == "" {
		triggerType = AdminScheduledJobTriggerManual
	}
	run := &AdminScheduledJobRun{
		JobID:           job.ID,
		TriggerType:     triggerType,
		Status:          AdminScheduledJobStatusRunning,
		StartedAt:       time.Now(),
		TriggeredByUser: req.TriggeredByUser,
	}
	return s.runRepo.Create(ctx, run)
}

func (s *AdminScheduledJobService) executeRun(ctx context.Context, job *AdminScheduledJob, run *AdminScheduledJobRun, triggerType string) {
	startedAt := time.Now()
	message, resultJSON, execErr := s.executor.Execute(ctx, job)
	finishedAt := time.Now()
	status := AdminScheduledJobStatusSuccess
	if execErr != nil {
		status = AdminScheduledJobStatusFailed
		if strings.TrimSpace(message) == "" {
			message = execErr.Error()
		}
	}
	_ = s.runRepo.UpdateFinished(ctx, run.ID, status, message, normalizeAdminScheduledJobResult(resultJSON), finishedAt)

	var nextRunAt time.Time
	if job.Enabled {
		nextRun, err := computeAdminScheduledJobNextRun(job.CronExpression, finishedAt)
		if err == nil {
			nextRunAt = nextRun
		}
	}
	_ = s.jobRepo.UpdateAfterRun(ctx, job.ID, startedAt, nextRunAt, status, message)
	_ = s.runRepo.PruneOldRuns(ctx, job.ID, job.RetentionLimit)
	if triggerType == AdminScheduledJobTriggerScheduled && !job.Enabled {
		return
	}
}

func validateAdminScheduledJob(jobType, cronExpr, payloadJSON string, retentionLimit int) error {
	switch strings.TrimSpace(jobType) {
	case AdminScheduledJobTypeBackup, AdminScheduledJobTypeDataManagementFull, AdminScheduledJobTypeChannelMonitorMaint, AdminScheduledJobTypeSyncCodexFreeGroups, AdminScheduledJobTypeCleanupErrorAccounts, AdminScheduledJobTypeUpdateOpenAIOAuthModelMapping, AdminScheduledJobTypeUpdateOpenAIOAuthSharedModelMapping, AdminScheduledJobTypeUpdateOpenAIOAuthExclusiveModelMapping:
	default:
		return fmt.Errorf("unsupported job type")
	}
	if _, err := computeAdminScheduledJobNextRun(cronExpr, time.Now()); err != nil {
		return fmt.Errorf("invalid cron expression: %w", err)
	}
	if retentionLimit <= 0 {
		retentionLimit = 100
	}
	if retentionLimit > 1000 {
		return fmt.Errorf("retention_limit too large")
	}
	if !json.Valid([]byte(normalizeAdminScheduledJobPayload(payloadJSON))) {
		return fmt.Errorf("payload_json must be valid json")
	}
	if err := validateAdminScheduledJobPayload(strings.TrimSpace(jobType), normalizeAdminScheduledJobPayload(payloadJSON)); err != nil {
		return err
	}
	return nil
}

func validateAdminScheduledJobPayload(jobType, payloadJSON string) error {
	switch jobType {
	case AdminScheduledJobTypeUpdateOpenAIOAuthModelMapping:
		return validateAdminScheduledOpenAIOAuthModelMappingPayload(payloadJSON)
	case AdminScheduledJobTypeUpdateOpenAIOAuthSharedModelMapping, AdminScheduledJobTypeUpdateOpenAIOAuthExclusiveModelMapping:
		return validateAdminScheduledOpenAIOAuthModelMappingPayload(payloadJSON)
	case AdminScheduledJobTypeSyncCodexFreeGroups:
		var payload adminScheduledSyncCodexFreeGroupsPayload
		if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
			return fmt.Errorf("invalid payload_json: %w", err)
		}
		if payload.SourceGroupID <= 0 {
			return fmt.Errorf("source_group_id is required")
		}
		if len(payload.TargetGroupIDs) == 0 {
			return fmt.Errorf("target_group_ids is required")
		}
		seen := make(map[int64]struct{}, len(payload.TargetGroupIDs))
		for _, groupID := range payload.TargetGroupIDs {
			if groupID <= 0 {
				return fmt.Errorf("target_group_ids contains invalid group id")
			}
			if groupID == payload.SourceGroupID {
				return fmt.Errorf("target_group_ids cannot contain source_group_id")
			}
			if _, exists := seen[groupID]; exists {
				return fmt.Errorf("target_group_ids contains duplicate group id")
			}
			seen[groupID] = struct{}{}
		}
	}
	return nil
}

func validateAdminScheduledOpenAIOAuthModelMappingPayload(payloadJSON string) error {
	var payload adminScheduledOpenAIOAuthModelMappingPayload
	if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
		return fmt.Errorf("invalid payload_json: %w", err)
	}
	if len(payload.ModelMapping) == 0 {
		return fmt.Errorf("model_mapping is required")
	}
	seen := make(map[string]struct{}, len(payload.ModelMapping))
	for source, target := range payload.ModelMapping {
		source = strings.TrimSpace(source)
		target = strings.TrimSpace(target)
		if source == "" || target == "" {
			return fmt.Errorf("model_mapping contains empty model")
		}
		if _, exists := seen[source]; exists {
			return fmt.Errorf("model_mapping contains duplicate source model")
		}
		seen[source] = struct{}{}
	}
	return nil
}

func computeAdminScheduledJobNextRun(cronExpr string, from time.Time) (time.Time, error) {
	sched, err := adminScheduledJobCronParser.Parse(strings.TrimSpace(cronExpr))
	if err != nil {
		return time.Time{}, err
	}
	return sched.Next(from), nil
}

func normalizeAdminScheduledJobPayload(payload string) string {
	trimmed := strings.TrimSpace(payload)
	if trimmed == "" {
		return "{}"
	}
	return trimmed
}

func normalizeAdminScheduledJobResult(result string) string {
	trimmed := strings.TrimSpace(result)
	if trimmed == "" {
		return "{}"
	}
	return trimmed
}

func normalizeAdminScheduledJobRetention(v int) int {
	if v <= 0 {
		return 100
	}
	return v
}
