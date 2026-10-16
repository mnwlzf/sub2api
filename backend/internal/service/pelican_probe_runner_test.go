package service

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// --- helpers ---

func pelicanPlanRow(id int64) *PelicanProbePlanRow {
	return &PelicanProbePlanRow{PelicanProbePlan: PelicanProbePlan{ID: id}}
}

func pelicanPlanRows(n int) []*PelicanProbePlanRow {
	rows := make([]*PelicanProbePlanRow, 0, n)
	for i := 1; i <= n; i++ {
		rows = append(rows, pelicanPlanRow(int64(i)))
	}
	return rows
}

// newTestPelicanRunner builds a runner with no repository dependency and an
// explicit jitter window, so tests never sleep on the production default.
func newTestPelicanRunner(maxWorkers int, startJitter time.Duration) *PelicanProbeRunnerService {
	jitter := startJitter
	return &PelicanProbeRunnerService{
		probeSvc:       &PelicanProbeService{},
		maxWorkers:     maxWorkers,
		startJitterMax: &jitter,
	}
}

// --- pelicanProbeJitter ---

func TestPelicanProbeJitter_WithinRange(t *testing.T) {
	const max = 250 * time.Millisecond
	for i := 0; i < 500; i++ {
		got := pelicanProbeJitter(max)
		require.GreaterOrEqual(t, got, time.Duration(0))
		require.Less(t, got, max)
	}
}

func TestPelicanProbeJitter_NonPositiveReturnsZero(t *testing.T) {
	// rand.Int64N panics on 0, so the guard must short-circuit before reaching it.
	require.Zero(t, pelicanProbeJitter(0))
	require.Zero(t, pelicanProbeJitter(-time.Second))
}

func TestPelicanProbeJitter_HasVariance(t *testing.T) {
	const max = 10 * time.Second
	seen := make(map[time.Duration]struct{}, 200)
	for i := 0; i < 200; i++ {
		seen[pelicanProbeJitter(max)] = struct{}{}
	}
	require.Greater(t, len(seen), 1, "jitter must not be a constant")
}

// --- defaults ---

func TestPelicanProbeDefaultMaxWorkers_LeavesHeadroomForUpstream(t *testing.T) {
	// The shared upstream pool rejects the 3rd concurrent probe with
	// "Concurrency limit exceeded for user, please retry later", and that
	// rejection is persisted as a real probe result. Raising this constant
	// without raising the upstream limit re-introduces the failure, so pin it.
	require.LessOrEqual(t, pelicanProbeDefaultMaxWorkers, 2,
		"upstream rejects the 3rd concurrent probe; keep at most 2 workers")
	require.Greater(t, pelicanProbeDefaultMaxWorkers, 0)
}

func TestPelicanProbeRunnerOverrides(t *testing.T) {
	// Unset overrides fall back to the production constants.
	r := &PelicanProbeRunnerService{}
	require.Equal(t, pelicanProbeDefaultMaxWorkers, r.workerLimit())
	require.Equal(t, pelicanProbeStartJitterMax, r.startJitter())
	require.Equal(t, pelicanProbeTickJitterMax, r.tickJitter())

	// An explicit zero must disable jitter instead of falling back, which is
	// what keeps the dispatch tests fast and deterministic.
	zero := time.Duration(0)
	r = &PelicanProbeRunnerService{startJitterMax: &zero, tickJitterMax: &zero, maxWorkers: 5}
	require.Equal(t, 5, r.workerLimit())
	require.Zero(t, r.startJitter())
	require.Zero(t, r.tickJitter())
}

// --- dispatchDuePlans ---

func TestDispatchDuePlans_CapsConcurrency(t *testing.T) {
	const workers = 2
	const plans = 6

	r := newTestPelicanRunner(workers, 0)

	var inFlight, peak, started int64
	var mu sync.Mutex

	r.dispatchDuePlans(context.Background(), pelicanPlanRows(plans), func(context.Context, *PelicanProbePlanRow) error {
		cur := atomic.AddInt64(&inFlight, 1)
		mu.Lock()
		if cur > peak {
			peak = cur
		}
		mu.Unlock()

		time.Sleep(10 * time.Millisecond)

		atomic.AddInt64(&inFlight, -1)
		atomic.AddInt64(&started, 1)
		return nil
	})

	require.EqualValues(t, plans, atomic.LoadInt64(&started), "every plan should run")
	mu.Lock()
	got := peak
	mu.Unlock()
	require.LessOrEqual(t, got, int64(workers), "concurrency must not exceed the worker limit")
}

func TestDispatchDuePlans_AppliesStartJitter(t *testing.T) {
	const workers = 4
	const jitter = 40 * time.Millisecond

	r := newTestPelicanRunner(workers, jitter)

	base := time.Now()
	var mu sync.Mutex
	delays := make([]time.Duration, 0, workers)

	r.dispatchDuePlans(context.Background(), pelicanPlanRows(workers), func(context.Context, *PelicanProbePlanRow) error {
		mu.Lock()
		delays = append(delays, time.Since(base))
		mu.Unlock()
		return nil
	})

	// Four independent draws from [0, 40ms): the odds that all of them land in
	// the first millisecond are (1/40)^4, so this assertion is not flaky.
	mu.Lock()
	var maxDelay time.Duration
	for _, d := range delays {
		if d > maxDelay {
			maxDelay = d
		}
	}
	mu.Unlock()
	require.Greater(t, int64(maxDelay), int64(time.Millisecond), "start jitter should stagger launches")
}

func TestDispatchDuePlans_SkipsPlanAlreadyRunning(t *testing.T) {
	r := newTestPelicanRunner(2, 0)

	// Occupy plan 1's guard, as an in-flight manual "run now" would.
	require.True(t, r.probeSvc.runGuard.tryStart(1))

	var mu sync.Mutex
	var ran []int64

	r.dispatchDuePlans(context.Background(), []*PelicanProbePlanRow{pelicanPlanRow(1), pelicanPlanRow(2)}, func(_ context.Context, p *PelicanProbePlanRow) error {
		mu.Lock()
		ran = append(ran, p.ID)
		mu.Unlock()
		return nil
	})

	mu.Lock()
	got := append([]int64(nil), ran...)
	mu.Unlock()
	require.Equal(t, []int64{2}, got, "a plan with an in-flight run must be skipped")

	// The skipped plan must still be held by the other run, not released here.
	require.False(t, r.probeSvc.runGuard.tryStart(1), "the guard of a skipped plan must stay held")
}

func TestDispatchDuePlans_ReleasesGuardsAfterRun(t *testing.T) {
	r := newTestPelicanRunner(2, 0)

	r.dispatchDuePlans(context.Background(), pelicanPlanRows(3), func(context.Context, *PelicanProbePlanRow) error {
		return nil
	})

	// Every finished plan must be runnable again on the next tick.
	for _, id := range []int64{1, 2, 3} {
		require.True(t, r.probeSvc.runGuard.tryStart(id), "plan %d should be released", id)
	}
}

func TestDispatchDuePlans_ContinuesAfterRunError(t *testing.T) {
	r := newTestPelicanRunner(2, 0)

	var started int64
	r.dispatchDuePlans(context.Background(), pelicanPlanRows(4), func(_ context.Context, p *PelicanProbePlanRow) error {
		atomic.AddInt64(&started, 1)
		if p.ID == 2 {
			return context.DeadlineExceeded
		}
		return nil
	})

	require.EqualValues(t, 4, atomic.LoadInt64(&started), "one failing plan must not stop the batch")
}
