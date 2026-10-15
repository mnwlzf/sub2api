//go:build unit

package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// handlerProbeEncryptor is a trivial SecretEncryptor for handler tests.
type handlerProbeEncryptor struct{}

func (handlerProbeEncryptor) Encrypt(plaintext string) (string, error) {
	return "enc:" + plaintext, nil
}
func (handlerProbeEncryptor) Decrypt(string) (string, error) { return "test-probe-key", nil }

// handlerProbeState is shared by the two repository doubles below (one Go type
// cannot implement both interfaces: both declare Create). It is mutex-guarded
// because the background run persists from its own goroutine.
type handlerProbeState struct {
	mu          sync.Mutex
	plan        *service.PelicanProbePlanRow
	runCount    int
	resultCount int
}

// completed reports whether the background run persisted its result.
func (s *handlerProbeState) completed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.runCount == 1 && s.resultCount == 1
}

type handlerPlanRepo struct{ state *handlerProbeState }

func (r handlerPlanRepo) Create(_ context.Context, plan *service.PelicanProbePlanRow) (*service.PelicanProbePlanRow, error) {
	cp := *plan
	return &cp, nil
}

func (r handlerPlanRepo) GetByID(_ context.Context, id int64) (*service.PelicanProbePlanRow, error) {
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	if r.state.plan == nil || r.state.plan.ID != id {
		return nil, sql.ErrNoRows
	}
	cp := *r.state.plan
	return &cp, nil
}

func (r handlerPlanRepo) List(_ context.Context, _ *int64) ([]*service.PelicanProbePlanRow, error) {
	return nil, nil
}

func (r handlerPlanRepo) ListDue(_ context.Context, _ time.Time) ([]*service.PelicanProbePlanRow, error) {
	return nil, nil
}

func (r handlerPlanRepo) ListPublic(_ context.Context) ([]*service.PelicanProbePlanRow, error) {
	return nil, nil
}

func (r handlerPlanRepo) Update(_ context.Context, plan *service.PelicanProbePlanRow) (*service.PelicanProbePlanRow, error) {
	cp := *plan
	return &cp, nil
}

func (r handlerPlanRepo) Delete(_ context.Context, _ int64) error { return nil }

func (r handlerPlanRepo) UpdateAfterRun(_ context.Context, _ int64, _, _ time.Time) error {
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	r.state.runCount++
	return nil
}

type handlerResultRepo struct{ state *handlerProbeState }

func (r handlerResultRepo) Create(_ context.Context, result *service.PelicanProbeResult) (*service.PelicanProbeResult, error) {
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	r.state.resultCount++
	cp := *result
	cp.ID = int64(r.state.resultCount)
	return &cp, nil
}

func (r handlerResultRepo) ListByPlanID(_ context.Context, _ int64, _ int) ([]*service.PelicanProbeResult, error) {
	return nil, nil
}

func (r handlerResultRepo) ListRecent(_ context.Context, _ *int64, _ int) ([]*service.PelicanProbeResult, error) {
	return nil, nil
}

func (r handlerResultRepo) ListPublicRecent(_ context.Context, _ int) ([]*service.PelicanProbeResult, error) {
	return nil, nil
}

func (r handlerResultRepo) Review(_ context.Context, _ int64, _, _, _ string) (*service.PelicanProbeResult, error) {
	return nil, nil
}

func (r handlerResultRepo) PruneOldResults(_ context.Context, _ int64, _ int) error { return nil }

func newProbeRunRouter(state *handlerProbeState) *gin.Engine {
	gin.SetMode(gin.TestMode)
	// The judge validation lookups are only exercised by the plan CRUD paths;
	// the async run-now endpoint under test needs none of them.
	svc := service.NewPelicanProbeService(
		handlerPlanRepo{state}, handlerResultRepo{state}, handlerProbeEncryptor{}, nil, nil,
		service.PelicanJudgeEnv{},
	)
	router := gin.New()
	router.POST("/admin/pelican-probe-plans/:id/run", NewPelicanProbeHandler(svc).RunNow)
	return router
}

func handlerProbeStateWithPlan(baseURL string) *handlerProbeState {
	return &handlerProbeState{plan: &service.PelicanProbePlanRow{
		PelicanProbePlan: service.PelicanProbePlan{
			ID: 1, GroupID: 1, ModelID: "m", BaseURL: baseURL,
			TestType: service.PelicanTestTypePelican, CronExpression: "0 */4 * * *",
			Enabled: true, MaxResults: 50,
		},
		Prompt: "custom prompt", APIKeyEncrypted: "enc:k",
	}}
}

// writeHandlerSSE serves a minimal OpenAI-compatible stream.
func writeHandlerSSE(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"<svg/>\"}}]}\n\n")
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
}

func TestPelicanProbeRunNowReturns202AndRunsInBackground(t *testing.T) {
	gate := make(chan struct{})
	var once sync.Once
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		<-gate
		writeHandlerSSE(w)
	}))
	defer srv.Close()
	defer once.Do(func() { close(gate) })

	state := handlerProbeStateWithPlan(srv.URL)
	router := newProbeRunRouter(state)

	req := httptest.NewRequest(http.MethodPost, "/admin/pelican-probe-plans/1/run", nil)
	ctx, cancel := context.WithCancel(req.Context())
	defer cancel()
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req.WithContext(ctx))

	require.Equal(t, http.StatusAccepted, rec.Code)
	var body struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			Started bool  `json:"started"`
			PlanID  int64 `json:"plan_id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, 0, body.Code)
	require.True(t, body.Data.Started)
	require.Equal(t, int64(1), body.Data.PlanID)
	require.False(t, state.completed(), "the trigger must answer before the probe finished")

	// The client goes away (frontend apiClient aborts at 30s): the probe must
	// still finish and land in the DB.
	cancel()
	once.Do(func() { close(gate) })

	require.Eventually(t, state.completed, 5*time.Second, 5*time.Millisecond,
		"the background run must complete after the request ended")
	require.EqualValues(t, 1, requests.Load())
}

func TestPelicanProbeRunNowConflictsWhileRunning(t *testing.T) {
	gate := make(chan struct{})
	var once sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-gate
		writeHandlerSSE(w)
	}))
	defer srv.Close()
	defer once.Do(func() { close(gate) })

	state := handlerProbeStateWithPlan(srv.URL)
	router := newProbeRunRouter(state)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/admin/pelican-probe-plans/1/run", nil))
	require.Equal(t, http.StatusAccepted, rec.Code)

	conflict := httptest.NewRecorder()
	router.ServeHTTP(conflict, httptest.NewRequest(http.MethodPost, "/admin/pelican-probe-plans/1/run", nil))
	require.Equal(t, http.StatusConflict, conflict.Code)

	var body struct {
		Code int `json:"code"`
		Data struct {
			Started bool   `json:"started"`
			PlanID  int64  `json:"plan_id"`
			Reason  string `json:"reason"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(conflict.Body.Bytes(), &body))
	require.Equal(t, http.StatusConflict, body.Code)
	require.False(t, body.Data.Started)
	require.Equal(t, int64(1), body.Data.PlanID)
	require.Equal(t, "already_running", body.Data.Reason)

	once.Do(func() { close(gate) })
	require.Eventually(t, state.completed, 5*time.Second, 5*time.Millisecond,
		"the accepted run must still complete")
}

func TestPelicanProbeRunNowErrorMapping(t *testing.T) {
	router := newProbeRunRouter(&handlerProbeState{})

	t.Run("invalid id", func(t *testing.T) {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/admin/pelican-probe-plans/abc/run", nil))
		require.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("unknown plan", func(t *testing.T) {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/admin/pelican-probe-plans/9/run", nil))
		require.Equal(t, http.StatusNotFound, rec.Code)
	})
}
