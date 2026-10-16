package service

import (
	"context"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/robfig/cron/v3"
)

const (
	// pelicanProbeDefaultMaxWorkers 限制同一 tick 内同时执行的探针数量。
	//
	// 上游账号（尤其是共享分组）的并发上限通常只有 2：并发 3 会让第 3 个
	// plan 直接被 "Concurrency limit exceeded for user, please retry later"
	// 打回，在用户端留下一条「未判为 21」的无效记录，还会被推进待裁判队列
	// 误导人工评审。压到 2 之后，多余的 plan 会排队等前面的完成，而不是
	// 同时去抢同一个并发名额。
	pelicanProbeDefaultMaxWorkers = 2

	// pelicanProbeTickOffset 把整分钟的 tick 推离 :00，避免和其他分钟级
	// cron 任务在同一秒争抢上游。
	pelicanProbeTickOffset = 25 * time.Second

	// pelicanProbeTickJitterMax 在 tick 偏移上叠加 [0, max) 的随机量，
	// 让相邻 tick 的启动时刻不再固定在同一个秒点。
	pelicanProbeTickJitterMax = 5 * time.Second

	// pelicanProbeStartJitterMax 是每个 plan 启动前的随机等待窗口。
	// 即使 maxWorkers 允许 2 个并发，也不该让它们在同一毫秒发起：把同一
	// tick 内的 plan 在时间轴上摊开，削掉瞬时并发尖峰。
	pelicanProbeStartJitterMax = 5 * time.Second
)

// pelicanProbeJitter 返回 [0, max) 内的均匀随机时长；max <= 0 时返回 0。
// rand.Int64N 在参数为 0 时会 panic，所以这里必须先挡掉非正数。
func pelicanProbeJitter(max time.Duration) time.Duration {
	if max <= 0 {
		return 0
	}
	return time.Duration(rand.Int64N(int64(max)))
}

// PelicanProbeRunnerService periodically scans due probe plans and executes them.
type PelicanProbeRunnerService struct {
	planRepo PelicanProbePlanRepository
	probeSvc *PelicanProbeService
	cfg      *config.Config

	cron      *cron.Cron
	startOnce sync.Once
	stopOnce  sync.Once

	// startJitterMax overrides pelicanProbeStartJitterMax when non-nil (tests).
	// A pointer distinguishes "unset" (nil -> use the constant) from an explicit
	// zero (disable jitter), which a plain duration cannot express.
	startJitterMax *time.Duration
	// tickJitterMax overrides pelicanProbeTickJitterMax when non-nil (tests).
	tickJitterMax *time.Duration
	// maxWorkers overrides pelicanProbeDefaultMaxWorkers when > 0 (tests).
	maxWorkers int
}

// startJitter returns the per-plan launch jitter window in effect.
func (s *PelicanProbeRunnerService) startJitter() time.Duration {
	if s.startJitterMax != nil {
		return *s.startJitterMax
	}
	return pelicanProbeStartJitterMax
}

// tickJitter returns the tick-offset jitter window in effect.
func (s *PelicanProbeRunnerService) tickJitter() time.Duration {
	if s.tickJitterMax != nil {
		return *s.tickJitterMax
	}
	return pelicanProbeTickJitterMax
}

// workerLimit returns the concurrent plan limit in effect.
func (s *PelicanProbeRunnerService) workerLimit() int {
	if s.maxWorkers > 0 {
		return s.maxWorkers
	}
	return pelicanProbeDefaultMaxWorkers
}

// NewPelicanProbeRunnerService creates a new runner.
func NewPelicanProbeRunnerService(
	planRepo PelicanProbePlanRepository,
	probeSvc *PelicanProbeService,
	cfg *config.Config,
) *PelicanProbeRunnerService {
	return &PelicanProbeRunnerService{
		planRepo: planRepo,
		probeSvc: probeSvc,
		cfg:      cfg,
	}
}

// Start begins the cron ticker (every minute).
func (s *PelicanProbeRunnerService) Start() {
	if s == nil {
		return
	}
	s.startOnce.Do(func() {
		loc := time.Local
		if s.cfg != nil {
			if parsed, err := time.LoadLocation(s.cfg.Timezone); err == nil && parsed != nil {
				loc = parsed
			}
		}
		c := cron.New(cron.WithParser(scheduledTestCronParser), cron.WithLocation(loc))
		_, err := c.AddFunc("* * * * *", func() { s.runScheduled() })
		if err != nil {
			logger.LegacyPrintf("service.pelican_probe_runner", "[PelicanProbeRunner] not started (invalid schedule): %v", err)
			return
		}
		s.cron = c
		s.cron.Start()
		logger.LegacyPrintf("service.pelican_probe_runner", "[PelicanProbeRunner] started (tick=every minute)")
	})
}

// Stop gracefully shuts down the cron scheduler.
func (s *PelicanProbeRunnerService) Stop() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() {
		if s.cron != nil {
			ctx := s.cron.Stop()
			select {
			case <-ctx.Done():
			case <-time.After(3 * time.Second):
				logger.LegacyPrintf("service.pelican_probe_runner", "[PelicanProbeRunner] cron stop timed out")
			}
		}
	})
}

func (s *PelicanProbeRunnerService) runScheduled() {
	// Offset from :00 to avoid thundering herd with other minutely jobs, plus
	// jitter so successive ticks do not all start on the same instant.
	time.Sleep(pelicanProbeTickOffset + pelicanProbeJitter(s.tickJitter()))

	ctx, cancel := context.WithTimeout(context.Background(), pelicanProbeRunTimeout)
	defer cancel()

	if !s.probeSvc.GetGlobalEnabled(ctx) {
		return
	}

	now := time.Now()
	plans, err := s.planRepo.ListDue(ctx, now)
	if err != nil {
		logger.LegacyPrintf("service.pelican_probe_runner", "[PelicanProbeRunner] ListDue error: %v", err)
		return
	}
	if len(plans) == 0 {
		return
	}
	logger.LegacyPrintf("service.pelican_probe_runner", "[PelicanProbeRunner] found %d due plans", len(plans))

	s.dispatchDuePlans(ctx, plans, func(ctx context.Context, p *PelicanProbePlanRow) error {
		_, err := s.probeSvc.runRow(ctx, p, true)
		return err
	})
}

// dispatchDuePlans runs fn for every plan with at most workerLimit concurrent
// executions, returning once all of them finish.
//
// The per-plan guard is shared with the manual trigger: a cron tick must never
// race an admin "run now" (or an overrunning previous tick) on the same plan.
// Each launch is staggered by startJitter so a single tick cannot fire every
// plan at the same instant — the upstream rejects excess concurrency with
// "Concurrency limit exceeded for user, please retry later", and that failure
// would otherwise be recorded as a real (invalid) probe result.
func (s *PelicanProbeRunnerService) dispatchDuePlans(
	ctx context.Context,
	plans []*PelicanProbePlanRow,
	fn func(context.Context, *PelicanProbePlanRow) error,
) {
	sem := make(chan struct{}, s.workerLimit())
	var wg sync.WaitGroup
	for _, plan := range plans {
		if !s.probeSvc.runGuard.tryStart(plan.ID) {
			logger.LegacyPrintf("service.pelican_probe_runner", "[PelicanProbeRunner] plan=%d skipped: a run is already in progress", plan.ID)
			continue
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(p *PelicanProbePlanRow) {
			defer wg.Done()
			defer func() { <-sem }()
			defer s.probeSvc.runGuard.finish(p.ID)
			// Stagger launches inside the worker so a tick's plans do not all
			// hit the upstream at the same instant. Sleeping here (instead of
			// before acquiring the slot) keeps the dispatcher loop from
			// serialising on jitter for every plan in the batch.
			if d := pelicanProbeJitter(s.startJitter()); d > 0 {
				time.Sleep(d)
			}
			if err := fn(ctx, p); err != nil {
				logger.LegacyPrintf("service.pelican_probe_runner", "[PelicanProbeRunner] plan=%d run error: %v", p.ID, err)
			}
		}(plan)
	}
	wg.Wait()
}
