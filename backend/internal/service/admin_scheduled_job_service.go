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
}

func NewAdminScheduledJobService(
	jobRepo AdminScheduledJobRepository,
	runRepo AdminScheduledJobRunRepository,
	executor AdminScheduledJobExecutor,
) *AdminScheduledJobService {
	return &AdminScheduledJobService{
		jobRepo:  jobRepo,
		runRepo:  runRepo,
		executor: executor,
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
	case AdminScheduledJobTypeBackup, AdminScheduledJobTypeDataManagementFull, AdminScheduledJobTypeChannelMonitorMaint:
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
