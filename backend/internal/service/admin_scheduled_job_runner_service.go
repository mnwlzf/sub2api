package service

import (
	"context"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/robfig/cron/v3"
)

type AdminScheduledJobRunnerService struct {
	service   *AdminScheduledJobService
	cfg       *config.Config
	cron      *cron.Cron
	startOnce sync.Once
	stopOnce  sync.Once
}

func NewAdminScheduledJobRunnerService(service *AdminScheduledJobService, cfg *config.Config) *AdminScheduledJobRunnerService {
	return &AdminScheduledJobRunnerService{service: service, cfg: cfg}
}

func (s *AdminScheduledJobRunnerService) Start() {
	if s == nil || s.service == nil {
		return
	}
	s.startOnce.Do(func() {
		loc := time.Local
		if s.cfg != nil {
			if parsed, err := time.LoadLocation(s.cfg.Timezone); err == nil && parsed != nil {
				loc = parsed
			}
		}
		c := cron.New(cron.WithParser(adminScheduledJobCronParser), cron.WithLocation(loc))
		_, err := c.AddFunc("* * * * *", func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			s.service.RunDueJobs(ctx, time.Now())
		})
		if err != nil {
			return
		}
		s.cron = c
		s.cron.Start()
	})
}

func (s *AdminScheduledJobRunnerService) Stop() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() {
		if s.cron != nil {
			<-s.cron.Stop().Done()
		}
	})
}
