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

// Pelican probe test types.
const (
	// PelicanTestTypePelican is the default pelican-SVG drawing test (human-reviewed).
	PelicanTestTypePelican = "pelican"
	// PelicanTestTypeCandy is the fixed-prompt candy combinatorics test (auto-judged).
	PelicanTestTypeCandy = "candy"
)

// PelicanAutoReviewer marks results judged automatically by the LLM judge.
const PelicanAutoReviewer = "auto"

// DefaultCandyPrompt is the fixed candy-test prompt. It must stay verbatim so
// candy results stay longitudinally comparable; users cannot customize it.
const DefaultCandyPrompt = `不使用任何外部工具回答以下问题：

在一个黑色的袋子里放有三种口味的糖果，每种糖果有两种不同的形状（圆形和五角星形，不同的形状靠手感可以分辨）。现已知不同口味的糖和不同形状的数量统计如下表。参赛者需要在活动前决定摸出的糖果数目，那么，最少取出多少个糖果才能保证手中同时拥有不同形状的苹果味和桃子味的糖？（同时手中有圆形苹果味匹配五角星桃子味糖果，或者有圆形桃子味匹配五角星苹果味糖果都满足要求）

        苹果味  桃子味  西瓜味
圆形       7      9      8
五角星形   7      6      4`

// DefaultCandyJudgePrompt is the fixed grading prompt for the candy test. The
// candidate answer is embedded at the %s placeholder, truncated to
// PelicanJudgeCandidateMaxRunes characters. The prompt constrains the reply to
// a single YES/NO word instead of a token limit, because max-token parameter
// names differ across upstream models.
const DefaultCandyJudgePrompt = `你是一个严格的判分员。以下是一道数学题的作答内容，请判断作答给出的最终答案是否为 21。

要求：
- 只看最终答案，不看过程
- 最终答案是 21（包括"二十一"等价表达）则回答 YES，否则回答 NO
- 你的回复必须且只能是 YES 或 NO 这一个单词，不要输出其他任何内容

作答内容：
---
%s
---`

// PelicanJudgeCandidateMaxRunes caps the candidate answer embedded in the
// judge prompt (counted in runes so multi-byte text is never split).
const PelicanJudgeCandidateMaxRunes = 4000

// PelicanProbePlan represents a group-level IQ probe plan.
type PelicanProbePlan struct {
	ID              int64      `json:"id"`
	Name            string     `json:"name"`
	GroupID         int64      `json:"group_id"`
	ModelID         string     `json:"model_id"`
	BaseURL         string     `json:"base_url"`
	HasAPIKey       bool       `json:"has_api_key"`
	TestType        string     `json:"test_type"`
	ReasoningEffort string     `json:"reasoning_effort"`
	JudgeModelID    string     `json:"judge_model_id"`
	CronExpression  string     `json:"cron_expression"`
	Enabled         bool       `json:"enabled"`
	IsPublic        bool       `json:"is_public"`
	GroupName       string     `json:"group_name,omitempty"`
	MaxResults      int        `json:"max_results"`
	LastRunAt       *time.Time `json:"last_run_at"`
	NextRunAt       *time.Time `json:"next_run_at"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
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
	Enabled bool                  `json:"enabled"`
	Plans   []*PelicanProbePlan   `json:"plans"`
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
