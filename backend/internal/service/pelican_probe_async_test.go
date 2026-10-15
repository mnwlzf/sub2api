package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// --- mutex-guarded repository doubles ---
//
// A background run touches the repositories from its own goroutine while the
// test asserts, so unlike the plain fakes in pelican_probe_service_test.go
// these are safe to share. The state lives in probeStore because a single Go
// type cannot implement both repository interfaces (both declare Create).

type probeStore struct {
	mu           sync.Mutex
	plans        map[int64]*PelicanProbePlanRow
	public       []*PelicanProbeResult
	results      []*PelicanProbeResult
	ranIDs       []int64
	nextPlanID   int64
	nextResultID int64
}

func newProbeStore() *probeStore {
	return &probeStore{plans: map[int64]*PelicanProbePlanRow{}}
}

// setPlan seeds a plan row.
func (s *probeStore) setPlan(row *PelicanProbePlanRow) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *row
	s.plans[row.ID] = &cp
}

// setPublicResults seeds the user-facing result list.
func (s *probeStore) setPublicResults(results []*PelicanProbeResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.public = results
}

// setResults seeds the admin-facing result list.
func (s *probeStore) setResults(results []*PelicanProbeResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.results = results
}

// resultCount reports how many results have been persisted.
func (s *probeStore) resultCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.results)
}

// resultsSnapshot copies the persisted results.
func (s *probeStore) resultsSnapshot() []*PelicanProbeResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*PelicanProbeResult, 0, len(s.results))
	for _, result := range s.results {
		cp := *result
		out = append(out, &cp)
	}
	return out
}

// ranIDsSnapshot copies the recorded schedule advances.
func (s *probeStore) ranIDsSnapshot() []int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]int64(nil), s.ranIDs...)
}

type storePlanRepo struct{ store *probeStore }

func (r storePlanRepo) Create(_ context.Context, plan *PelicanProbePlanRow) (*PelicanProbePlanRow, error) {
	s := r.store
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextPlanID++
	cp := *plan
	cp.ID = s.nextPlanID
	s.plans[cp.ID] = &cp
	out := cp
	return &out, nil
}

func (r storePlanRepo) GetByID(_ context.Context, id int64) (*PelicanProbePlanRow, error) {
	s := r.store
	s.mu.Lock()
	defer s.mu.Unlock()
	plan, ok := s.plans[id]
	if !ok {
		return nil, sql.ErrNoRows
	}
	cp := *plan
	return &cp, nil
}

func (r storePlanRepo) List(_ context.Context, _ *int64) ([]*PelicanProbePlanRow, error) {
	s := r.store
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*PelicanProbePlanRow, 0, len(s.plans))
	for _, plan := range s.plans {
		cp := *plan
		out = append(out, &cp)
	}
	return out, nil
}

func (r storePlanRepo) ListDue(_ context.Context, _ time.Time) ([]*PelicanProbePlanRow, error) {
	return nil, nil
}

func (r storePlanRepo) ListPublic(_ context.Context) ([]*PelicanProbePlanRow, error) {
	s := r.store
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*PelicanProbePlanRow, 0, len(s.plans))
	for _, plan := range s.plans {
		if !plan.IsPublic || !plan.Enabled {
			continue
		}
		cp := *plan
		out = append(out, &cp)
	}
	return out, nil
}

func (r storePlanRepo) Update(_ context.Context, plan *PelicanProbePlanRow) (*PelicanProbePlanRow, error) {
	s := r.store
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *plan
	s.plans[cp.ID] = &cp
	out := cp
	return &out, nil
}

func (r storePlanRepo) Delete(_ context.Context, id int64) error {
	s := r.store
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.plans, id)
	return nil
}

func (r storePlanRepo) UpdateAfterRun(_ context.Context, id int64, _, _ time.Time) error {
	s := r.store
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ranIDs = append(s.ranIDs, id)
	return nil
}

type storeResultRepo struct{ store *probeStore }

func (r storeResultRepo) Create(ctx context.Context, result *PelicanProbeResult) (*PelicanProbeResult, error) {
	// Mimic database/sql: a cancelled context fails the write.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s := r.store
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextResultID++
	cp := *result
	cp.ID = s.nextResultID
	s.results = append(s.results, &cp)
	out := cp
	return &out, nil
}

func (r storeResultRepo) ListByPlanID(_ context.Context, planID int64, _ int) ([]*PelicanProbeResult, error) {
	s := r.store
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*PelicanProbeResult, 0, len(s.results))
	for _, result := range s.results {
		if result.PlanID != planID {
			continue
		}
		cp := *result
		out = append(out, &cp)
	}
	return out, nil
}

func (r storeResultRepo) ListRecent(_ context.Context, _ *int64, _ int) ([]*PelicanProbeResult, error) {
	s := r.store
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*PelicanProbeResult, 0, len(s.results))
	for _, result := range s.results {
		cp := *result
		out = append(out, &cp)
	}
	return out, nil
}

func (r storeResultRepo) ListPublicRecent(_ context.Context, _ int) ([]*PelicanProbeResult, error) {
	s := r.store
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*PelicanProbeResult, 0, len(s.public))
	for _, result := range s.public {
		cp := *result
		out = append(out, &cp)
	}
	return out, nil
}

func (r storeResultRepo) Review(_ context.Context, _ int64, _, _, _ string) (*PelicanProbeResult, error) {
	return nil, nil
}

func (r storeResultRepo) PruneOldResults(ctx context.Context, _ int64, _ int) error {
	return ctx.Err()
}

// asyncProbeService wires a probe service to the shared store.
func asyncProbeService(srv *httptest.Server) (*PelicanProbeService, *probeStore) {
	store := newProbeStore()
	return &PelicanProbeService{
		planRepo: storePlanRepo{store}, resultRepo: storeResultRepo{store},
		encryptor: fakePelicanEncryptor{}, httpClient: srv.Client(),
	}, store
}

// blockingProbeServer serves a stream but holds every request until the gate is
// closed, so a test can observe the in-flight window deterministically. The
// returned release func is idempotent and safe to defer.
func blockingProbeServer(t *testing.T, requests *atomic.Int32, chunks ...string) (*httptest.Server, func()) {
	t.Helper()
	gate := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(gate) }) }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		<-gate
		writeProbeSSE(w, chunks...)
	}))
	return srv, release
}

// --- task 1: asynchronous "run now" ---

func TestStartPlanRunIsAsyncAndCompletesInBackground(t *testing.T) {
	var requests atomic.Int32
	srv, release := blockingProbeServer(t, &requests, probeDeltaChunk("<svg>pelican</svg>"), probeUsageChunk(11, 22))
	defer srv.Close()
	defer release()

	svc, store := asyncProbeService(srv)
	store.setPlan(probeRow(srv.URL))

	start := time.Now()
	trigger, err := svc.StartPlanRun(context.Background(), 1)
	require.NoError(t, err)
	require.True(t, trigger.Started)
	require.Equal(t, int64(1), trigger.PlanID)
	require.Empty(t, trigger.Reason)
	require.Less(t, time.Since(start), 2*time.Second, "the trigger must not wait for the probe")
	require.Zero(t, store.resultCount(), "nothing may be persisted while the upstream is still generating")

	release()
	require.Eventually(t, func() bool { return store.resultCount() == 1 }, 5*time.Second, 5*time.Millisecond)

	saved := store.resultsSnapshot()[0]
	require.Equal(t, PelicanRunSuccess, saved.Status)
	require.Equal(t, "<svg>pelican</svg>", saved.RawHTML)
	require.Equal(t, 11, saved.PromptTokens)
	require.Equal(t, 22, saved.CompletionTokens)
	require.EqualValues(t, 1, requests.Load())
	require.Equal(t, []int64{1}, store.ranIDsSnapshot(), "a manual run advances the schedule")
}

// TestStartPlanRunSurvivesRequestCancel is the regression for the production
// bug: the frontend apiClient aborts the trigger request after 30s, which used
// to cancel the probe itself (`context canceled` on every manual run).
func TestStartPlanRunSurvivesRequestCancel(t *testing.T) {
	var requests atomic.Int32
	srv, release := blockingProbeServer(t, &requests, probeDeltaChunk("<svg>pelican</svg>"))
	defer srv.Close()
	defer release()

	svc, store := asyncProbeService(srv)
	store.setPlan(probeRow(srv.URL))

	ctx, cancel := context.WithCancel(context.Background())
	trigger, err := svc.StartPlanRun(ctx, 1)
	require.NoError(t, err)
	require.True(t, trigger.Started)

	// The browser is gone (30s client timeout / disconnect) before the model
	// answered a single byte.
	cancel()
	release()

	require.Eventually(t, func() bool { return store.resultCount() == 1 }, 5*time.Second, 5*time.Millisecond)
	saved := store.resultsSnapshot()[0]
	require.Equal(t, PelicanRunSuccess, saved.Status, "the detached run must survive request cancellation")
	require.Equal(t, "<svg>pelican</svg>", saved.RawHTML)
	require.Empty(t, saved.ErrorMessage)
}

// TestStartPlanRunCandyJudgeUsesBackgroundContext pins that the judge call also
// runs on the detached context (a cancelled request must not turn a decidable
// candy run into a pending one).
func TestStartPlanRunCandyJudgeUsesBackgroundContext(t *testing.T) {
	var probeReqs, judgeReqs atomic.Int32
	gate := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(gate) }) }

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var decoded map[string]any
		_ = json.Unmarshal(body, &decoded)
		if model, _ := decoded["model"].(string); model == "judge-model" {
			judgeReqs.Add(1)
			writeChatContent(w, "YES")
			return
		}
		probeReqs.Add(1)
		<-gate
		writeProbeSSE(w, probeDeltaChunk("答案是 21"))
	}))
	defer srv.Close()
	defer release()

	svc, store := asyncProbeService(srv)
	store.setPlan(candyJudgeRow(srv.URL))

	ctx, cancel := context.WithCancel(context.Background())
	trigger, err := svc.StartPlanRun(ctx, 1)
	require.NoError(t, err)
	require.True(t, trigger.Started)
	cancel()
	release()

	require.Eventually(t, func() bool { return store.resultCount() == 1 }, 5*time.Second, 5*time.Millisecond)
	saved := store.resultsSnapshot()[0]
	require.Equal(t, PelicanRunSuccess, saved.Status)
	require.Equal(t, PelicanVerdictPass, saved.Verdict, "the judge must run on the background context")
	require.Contains(t, saved.Note, "judge=judge-model: YES")
	require.EqualValues(t, 1, probeReqs.Load())
	require.EqualValues(t, 1, judgeReqs.Load())
}

func TestStartPlanRunRejectsConcurrentRunForSamePlan(t *testing.T) {
	var requests atomic.Int32
	srv, release := blockingProbeServer(t, &requests, probeDeltaChunk("<svg/>"))
	defer srv.Close()
	defer release()

	svc, store := asyncProbeService(srv)
	store.setPlan(probeRow(srv.URL))
	otherPlan := probeRow(srv.URL)
	otherPlan.ID = 2
	store.setPlan(otherPlan)

	first, err := svc.StartPlanRun(context.Background(), 1)
	require.NoError(t, err)
	require.True(t, first.Started)

	second, err := svc.StartPlanRun(context.Background(), 1)
	require.ErrorIs(t, err, ErrPelicanProbeAlreadyRunning)
	require.Nil(t, second)

	// The guard is per plan: a different plan may start concurrently.
	third, err := svc.StartPlanRun(context.Background(), 2)
	require.NoError(t, err)
	require.True(t, third.Started)

	release()
	require.Eventually(t, func() bool { return store.resultCount() == 2 }, 5*time.Second, 5*time.Millisecond)
	require.EqualValues(t, 2, requests.Load(), "the refused trigger must never reach the gateway")

	// The slot is released once the run finished, so the plan can run again.
	require.Eventually(t, func() bool {
		if svc.runGuard.tryStart(1) {
			svc.runGuard.finish(1)
			return true
		}
		return false
	}, 5*time.Second, 5*time.Millisecond)
}

func TestStartPlanRunValidatesBeforeStarting(t *testing.T) {
	t.Run("unknown plan", func(t *testing.T) {
		srv, release := blockingProbeServer(t, new(atomic.Int32))
		defer srv.Close()
		defer release()

		svc, _ := asyncProbeService(srv)
		trigger, err := svc.StartPlanRun(context.Background(), 42)
		require.ErrorIs(t, err, ErrPelicanProbePlanNotFound)
		require.Nil(t, trigger)
	})

	t.Run("invalid test type", func(t *testing.T) {
		var requests atomic.Int32
		srv, release := blockingProbeServer(t, &requests)
		defer srv.Close()
		defer release()

		svc, store := asyncProbeService(srv)
		row := probeRow(srv.URL)
		row.TestType = "vlm"
		store.setPlan(row)

		_, err := svc.StartPlanRun(context.Background(), 1)
		require.ErrorContains(t, err, "invalid test_type")
		require.Zero(t, store.resultCount())
		require.Zero(t, requests.Load())
	})
}

func TestRunGuardIsPerPlan(t *testing.T) {
	var guard pelicanProbeRunGuard
	require.True(t, guard.tryStart(1))
	require.False(t, guard.tryStart(1))
	require.True(t, guard.tryStart(2))
	guard.finish(1)
	require.True(t, guard.tryStart(1))
	guard.finish(2)
}

// --- task 4: public output never carries internal errors ---

func TestClassifyProbeFailure(t *testing.T) {
	cases := []struct {
		name string
		msg  string
		want string
	}{
		{
			name: "production: request context cancelled",
			msg:  `Post "http://127.0.0.1:8080/v1/chat/completions": context canceled`,
			want: PelicanFailureTimeout,
		},
		{
			name: "production: client timeout awaiting headers",
			msg:  `Post "http://127.0.0.1:8080/v1/chat/completions": context deadline exceeded (Client.Timeout exceeded while awaiting headers)`,
			want: PelicanFailureTimeout,
		},
		{
			name: "production: concurrency limit",
			msg:  `upstream HTTP 429: {"code":"gateway_concurrency_limit","message":"Concurrency limit exceeded for user"}`,
			want: PelicanFailureConcurrency,
		},
		{
			name: "queue full",
			msg:  `upstream HTTP 503: {"code":"gateway_queue_full"}`,
			want: PelicanFailureConcurrency,
		},
		{
			name: "model not found",
			msg:  `upstream HTTP 404: {"error":{"code":"model_not_found"}}`,
			want: PelicanFailureConfig,
		},
		{
			name: "rejected key",
			msg:  `upstream HTTP 401: {"error":{"message":"invalid api key"}}`,
			want: PelicanFailureConfig,
		},
		{
			name: "upstream 5xx",
			msg:  "upstream HTTP 500: boom",
			want: PelicanFailureUpstream,
		},
		{
			name: "empty content",
			msg:  "empty model content",
			want: PelicanFailureUpstream,
		},
		{
			name: "unclassified",
			msg:  "something nobody has seen before",
			want: PelicanFailureUnknown,
		},
		{name: "no error", msg: "", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, classifyProbeFailure(tc.msg))
		})
	}
}

func TestPublicResultNote(t *testing.T) {
	require.Equal(t, "judge=deepseek-chat: YES", publicResultNote("judge=deepseek-chat: YES"))
	require.Equal(t, "judge=deepseek-chat: NO", publicResultNote("judge=deepseek-chat: NO | reply: 答案是 20"))
	require.Empty(t, publicResultNote(""))
	require.Empty(t, publicResultNote("auto-judge skipped: judge_model_id not configured (awaiting manual review)"))
	require.Empty(t, publicResultNote(
		`auto-judge skipped: judge HTTP 500: Post "http://127.0.0.1:8080/v1/chat/completions": context canceled`))
}

func TestListPublicStatusRedactsInternalErrors(t *testing.T) {
	store := newProbeStore()
	plan := probeRow("http://127.0.0.1:8080")
	plan.ID = 7
	plan.IsPublic = true
	store.setPlan(plan)
	seeded := []*PelicanProbeResult{
		{
			ID: 1, PlanID: 7, Status: PelicanRunFailed,
			ErrorMessage: `Post "http://127.0.0.1:8080/v1/chat/completions": context canceled`,
		},
		{
			ID: 2, PlanID: 7, Status: PelicanRunFailed,
			ErrorMessage: `upstream HTTP 429: {"code":"gateway_concurrency_limit","message":"Concurrency limit exceeded for user"}`,
		},
		{
			ID: 3, PlanID: 7, Status: PelicanRunFailed,
			ErrorMessage: `upstream HTTP 404: {"error":{"code":"model_not_found"}}`,
		},
		{
			ID: 4, PlanID: 7, Status: PelicanRunSuccess,
			Note: "judge=deepseek-chat: YES | reply: YES",
		},
		{
			ID: 5, PlanID: 7, Status: PelicanRunSuccess,
			Note: `auto-judge skipped: judge HTTP 500: Post "http://127.0.0.1:8080/v1/chat/completions": context canceled`,
		},
	}
	store.setPublicResults(seeded)
	store.setResults(seeded)

	svc := &PelicanProbeService{
		planRepo: storePlanRepo{store}, resultRepo: storeResultRepo{store},
		encryptor: fakePelicanEncryptor{},
	}
	status, err := svc.ListPublicStatus(context.Background())
	require.NoError(t, err)
	require.True(t, status.Enabled)

	require.Len(t, status.Plans, 1)
	require.Empty(t, status.Plans[0].BaseURL, "the probe base url is internal topology")

	byID := map[int64]*PelicanProbeResult{}
	for _, result := range status.Results {
		byID[result.ID] = result
	}
	require.Len(t, byID, 5)
	for id, result := range byID {
		require.Emptyf(t, result.ErrorMessage, "result %d leaked the raw upstream error", id)
	}
	require.Equal(t, PelicanFailureTimeout, byID[1].FailureReason)
	require.Equal(t, PelicanFailureConcurrency, byID[2].FailureReason)
	require.Equal(t, PelicanFailureConfig, byID[3].FailureReason)
	require.Empty(t, byID[4].FailureReason, "a successful run has no failure reason")
	require.Equal(t, "judge=deepseek-chat: YES", byID[4].Note)
	require.Empty(t, byID[5].Note, "skip notes can quote internal errors and are dropped")

	blob, err := json.Marshal(status)
	require.NoError(t, err)
	require.NotContains(t, string(blob), "127.0.0.1")
	require.NotContains(t, string(blob), "gateway_concurrency_limit")
	require.NotContains(t, string(blob), "v1/chat/completions")

	// Admin endpoints keep the verbatim material.
	adminResults, err := svc.ListRecentResults(context.Background(), nil, 10)
	require.NoError(t, err)
	var foundRaw bool
	for _, result := range adminResults {
		if strings.Contains(result.ErrorMessage, "127.0.0.1") && result.FailureReason == "" {
			foundRaw = true
		}
	}
	require.True(t, foundRaw, "the admin view must still show the raw upstream error")

	adminPlans, err := svc.ListPlans(context.Background(), nil)
	require.NoError(t, err)
	require.Len(t, adminPlans, 1)
	require.Equal(t, "http://127.0.0.1:8080", adminPlans[0].BaseURL)
}
