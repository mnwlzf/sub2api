package service

import (
	"context"
	"time"
)

// Pelican probe verdicts (human-reviewed).
const (
	PelicanVerdictPending  = "pending"
	PelicanVerdictPass     = "pass"
	PelicanVerdictDegraded = "degraded"
)

// Pelican probe run status.
const (
	PelicanRunSuccess = "success"
	PelicanRunFailed  = "failed"
)

// DefaultPelicanPrompt is the Classic-track prompt. All plans share it by
// default so scores stay longitudinally comparable for the same group+model.
const DefaultPelicanPrompt = "创建一个HTML，内容是SVG绘制一个鹈鹕骑自行车的2D动画"

// PelicanProbePlan represents a group-level IQ probe plan.
type PelicanProbePlan struct {
	ID             int64      `json:"id"`
	Name           string     `json:"name"`
	GroupID        int64      `json:"group_id"`
	ModelID        string     `json:"model_id"`
	BaseURL        string     `json:"base_url"`
	HasAPIKey      bool       `json:"has_api_key"`
	CronExpression string     `json:"cron_expression"`
	Enabled        bool       `json:"enabled"`
	IsPublic       bool       `json:"is_public"`
	GroupName      string     `json:"group_name,omitempty"`
	MaxResults     int        `json:"max_results"`
	LastRunAt      *time.Time `json:"last_run_at"`
	NextRunAt      *time.Time `json:"next_run_at"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// PelicanProbePlanRow is the internal representation including secrets.
// It is exported because the repository package must scan it.
type PelicanProbePlanRow struct {
	PelicanProbePlan
	Prompt          string
	APIKeyEncrypted string
}

// PelicanProbeResult represents a single probe execution result.
type PelicanProbeResult struct {
	ID               int64      `json:"id"`
	PlanID           int64      `json:"plan_id"`
	Status           string     `json:"status"`
	RawHTML          string     `json:"raw_html"`
	Verdict          string     `json:"verdict"`
	Note             string     `json:"note"`
	ReviewedBy       string     `json:"reviewed_by"`
	ReviewedAt       *time.Time `json:"reviewed_at"`
	ErrorMessage     string     `json:"error_message"`
	LatencyMs        int64      `json:"latency_ms"`
	PromptTokens     int        `json:"prompt_tokens"`
	CompletionTokens int        `json:"completion_tokens"`
	StartedAt        time.Time  `json:"started_at"`
	FinishedAt       time.Time  `json:"finished_at"`
	CreatedAt        time.Time  `json:"created_at"`
}

// PelicanProbePublicStatus is the user-facing payload: only plans explicitly
// marked public, plus their recent results. Never includes secrets.
type PelicanProbePublicStatus struct {
	Enabled bool                 `json:"enabled"`
	Plans   []*PelicanProbePlan  `json:"plans"`
	Results []*PelicanProbeResult `json:"results"`
}

// PelicanProbePlanRepository defines data access for probe plans.
type PelicanProbePlanRepository interface {
	Create(ctx context.Context, plan *PelicanProbePlanRow) (*PelicanProbePlanRow, error)
	GetByID(ctx context.Context, id int64) (*PelicanProbePlanRow, error)
	List(ctx context.Context, groupID *int64) ([]*PelicanProbePlanRow, error)
	ListDue(ctx context.Context, now time.Time) ([]*PelicanProbePlanRow, error)
	ListPublic(ctx context.Context) ([]*PelicanProbePlanRow, error)
	Update(ctx context.Context, plan *PelicanProbePlanRow) (*PelicanProbePlanRow, error)
	Delete(ctx context.Context, id int64) error
	UpdateAfterRun(ctx context.Context, id int64, lastRunAt time.Time, nextRunAt time.Time) error
}

// PelicanProbeResultRepository defines data access for probe results.
type PelicanProbeResultRepository interface {
	Create(ctx context.Context, result *PelicanProbeResult) (*PelicanProbeResult, error)
	ListByPlanID(ctx context.Context, planID int64, limit int) ([]*PelicanProbeResult, error)
	ListRecent(ctx context.Context, groupID *int64, limit int) ([]*PelicanProbeResult, error)
	ListPublicRecent(ctx context.Context, limit int) ([]*PelicanProbeResult, error)
	Review(ctx context.Context, id int64, verdict, note, reviewer string) (*PelicanProbeResult, error)
	PruneOldResults(ctx context.Context, planID int64, keepCount int) error
}
