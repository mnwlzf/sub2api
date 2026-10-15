package service

import (
	"context"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/robfig/cron/v3"
)

const pelicanProbeDefaultMaxWorkers = 3

// PelicanProbeRunnerService periodically scans due probe plans and executes them.
type PelicanProbeRunnerService struct {
	planRepo PelicanProbePlanRepository
	probeSvc *PelicanProbeService
	cfg      *config.Config

	cron      *cron.Cron
	startOnce sync.Once
	stopOnce  sync.Once
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
	// Offset from :00 to avoid thundering herd with other minutely jobs.
	time.Sleep(25 * time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
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

	sem := make(chan struct{}, pelicanProbeDefaultMaxWorkers)
	var wg sync.WaitGroup
	for _, plan := range plans {
		sem <- struct{}{}
		wg.Add(1)
		go func(p *PelicanProbePlanRow) {
			defer wg.Done()
			defer func() { <-sem }()
			if _, err := s.probeSvc.runRow(ctx, p, true); err != nil {
				logger.LegacyPrintf("service.pelican_probe_runner", "[PelicanProbeRunner] plan=%d run error: %v", p.ID, err)
			}
		}(plan)
	}
	wg.Wait()
}
