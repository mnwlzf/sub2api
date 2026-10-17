package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestExtractProbeHTML(t *testing.T) {
	// production shape: the model narrates, then hands over the document in a
	// fenced block. The old implementation only stripped a fence when the whole
	// answer started with one, so this exact input was stored as "prose + code"
	// and rendered as text inside the iframe.
	const narrated = "我先看一下工作区现有文件，确认新 HTML 放在哪里。\n\n" +
		"将以下代码保存为 `pelican-bike.html`：\n\n" +
		"```html\n<!DOCTYPE html>\n<html><body>bike</body></html>\n```\n"

	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "narrated prose then fenced html",
			in:   narrated,
			want: "<!DOCTYPE html>\n<html><body>bike</body></html>",
		},
		{name: "plain html untouched", in: `<html><body>hi</body></html>`, want: `<html><body>hi</body></html>`},
		{name: "fenced html", in: "```html\n<html></html>\n```", want: `<html></html>`},
		{name: "bare fence", in: "```\n<div/>\n```", want: `<div/>`},
		{name: "tilde fence", in: "~~~html\n<svg/>\n~~~", want: `<svg/>`},
		{name: "uppercase language tag", in: "```HTML\n<svg/>\n```", want: `<svg/>`},
		{name: "indented fence after prose", in: "这是结果：\n\n  ```html\n<svg/>\n  ```\n", want: `<svg/>`},
		{name: "surrounding whitespace", in: "  ```html\n<a/>\n```  \n", want: `<a/>`},
		{name: "no closing fence keeps body", in: "```html\n<a/>", want: `<a/>`},
		{name: "unclosed fence after prose", in: "给你：\n```html\n<svg/>", want: `<svg/>`},
		{
			name: "multiple blocks prefers the html-tagged one",
			in:   "先看这个：\n```\nnot markup\n```\n\n再是这个：\n```html\n<svg/>\n```\n",
			want: `<svg/>`,
		},
		{name: "multiple untagged blocks take the first", in: "```\nA\n```\n```\nB\n```", want: `A`},
		{name: "empty blocks are skipped", in: "```html\n\n```\n```html\n<svg/>\n```", want: `<svg/>`},
		{
			name: "no fence but doctype marker",
			in:   "这是成品：\n\n<!DOCTYPE html>\n<html><body>x</body></html>\n",
			want: "<!DOCTYPE html>\n<html><body>x</body></html>",
		},
		{name: "no fence but svg marker", in: "这是图：<svg><rect/></svg>", want: `<svg><rect/></svg>`},
		{name: "marker match is case-insensitive", in: "说点什么<HTML></HTML>", want: `<HTML></HTML>`},
		{name: "earliest marker wins", in: "a <body>b</body> <svg/>", want: "<body>b</body> <svg/>"},
		{name: "one-line fenced html drops the stray fence", in: "```html <svg/>```", want: `<svg/>`},
		{name: "plain prose is kept as-is", in: "答案是 21", want: "答案是 21"},
		{name: "inline code untouched", in: "use `code` here", want: "use `code` here"},
		{name: "empty content", in: "", want: ""},
		{name: "whitespace only", in: "  \n\t ", want: ""},
		{name: "fence only", in: "```html", want: "```html"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, extractProbeHTML(tc.in))
		})
	}

	// Deterministic: the same answer always yields the same document, so a
	// re-run of a probe never renders something different.
	t.Run("deterministic", func(t *testing.T) {
		first := extractProbeHTML(narrated)
		for i := 0; i < 50; i++ {
			require.Equal(t, first, extractProbeHTML(narrated))
		}
	})
}

// TestExecuteProbeExtractsHTMLOnlyForPelican pins the asymmetry: the pelican
// answer becomes a renderable document, while the candy answer is stored
// verbatim because the judge must read the model's full reply.
func TestExecuteProbeExtractsHTMLOnlyForPelican(t *testing.T) {
	const answer = "答案是 21，因为……\n\n```\n21\n```\n"

	t.Run("pelican extracts the fenced document", func(t *testing.T) {
		var captured map[string]any
		srv := probeTestServer(t, &captured, answer, false)
		defer srv.Close()
		svc, _, _ := probeSvcWithServer(t, srv)

		raw, _, _, _, err := svc.executeProbe(context.Background(), probeRow(srv.URL))
		require.NoError(t, err)
		require.Equal(t, "21", raw)
	})

	t.Run("candy keeps the raw answer", func(t *testing.T) {
		var captured map[string]any
		srv := probeTestServer(t, &captured, answer, false)
		defer srv.Close()
		svc, _, _ := probeSvcWithServer(t, srv)

		row := probeRow(srv.URL)
		row.TestType = PelicanTestTypeCandy
		raw, _, _, _, err := svc.executeProbe(context.Background(), row)
		require.NoError(t, err)
		require.Equal(t, strings.TrimSpace(answer), raw)
		require.Contains(t, raw, "答案是 21")
	})
}

func TestPelicanProbeCreatePlanValidation(t *testing.T) {
	svc := &PelicanProbeService{}
	ctx := context.Background()

	_, err := svc.CreatePlan(ctx, CreatePlanInput{ModelID: "m", APIKey: "k"})
	require.ErrorContains(t, err, "group_id is required")

	_, err = svc.CreatePlan(ctx, CreatePlanInput{GroupID: 1, APIKey: "k"})
	require.ErrorContains(t, err, "model_id is required")

	_, err = svc.CreatePlan(ctx, CreatePlanInput{GroupID: 1, ModelID: "m"})
	require.ErrorContains(t, err, "api_key is required")

	_, err = svc.CreatePlan(ctx, CreatePlanInput{GroupID: 1, ModelID: "m", APIKey: "k", CronExpression: "not-a-cron"})
	require.ErrorContains(t, err, "invalid cron expression")
}

func TestPelicanProbeReviewValidation(t *testing.T) {
	svc := &PelicanProbeService{}
	_, err := svc.ReviewResult(context.Background(), 1, "maybe", "", "")
	require.ErrorContains(t, err, "verdict must be pass or degraded")
}

func TestSanitizePlan(t *testing.T) {
	p := sanitizePlan(&PelicanProbePlanRow{APIKeyEncrypted: "cipher"})
	require.True(t, p.HasAPIKey)

	p = sanitizePlan(&PelicanProbePlanRow{})
	require.False(t, p.HasAPIKey)
}

func TestNormalizePelicanTestType(t *testing.T) {
	got, err := normalizePelicanTestType("")
	require.NoError(t, err)
	require.Equal(t, PelicanTestTypePelican, got)

	got, err = normalizePelicanTestType("pelican")
	require.NoError(t, err)
	require.Equal(t, PelicanTestTypePelican, got)

	got, err = normalizePelicanTestType("  CANDY ")
	require.NoError(t, err)
	require.Equal(t, PelicanTestTypeCandy, got)

	_, err = normalizePelicanTestType("vlm")
	require.ErrorContains(t, err, "invalid test_type")
}

func TestNormalizePelicanReasoningEffort(t *testing.T) {
	got, err := normalizePelicanReasoningEffort("")
	require.NoError(t, err)
	require.Empty(t, got)

	got, err = normalizePelicanReasoningEffort("  HIGH ")
	require.NoError(t, err)
	require.Equal(t, "high", got)

	got, err = normalizePelicanReasoningEffort("xhigh")
	require.NoError(t, err)
	require.Equal(t, "xhigh", got)

	_, err = normalizePelicanReasoningEffort("ultra")
	require.ErrorContains(t, err, "invalid reasoning_effort")
}

func TestParseJudgeVerdict(t *testing.T) {
	cases := []struct {
		name  string
		reply string
		want  string
	}{
		{name: "upper YES", reply: "YES", want: PelicanJudgeYes},
		{name: "upper NO", reply: "NO", want: PelicanJudgeNo},
		{name: "lower yes", reply: "yes", want: PelicanJudgeYes},
		{name: "mixed no", reply: "No", want: PelicanJudgeNo},
		{name: "surrounding whitespace", reply: "  YES\n", want: PelicanJudgeYes},
		{name: "cjk boundary", reply: "判定：YES。", want: PelicanJudgeYes},
		{name: "sentence with punctuation", reply: "The final answer is 21, so: YES.", want: PelicanJudgeYes},
		{name: "both words ambiguous", reply: "YES and NO", want: ""},
		{name: "both words reversed order", reply: "NO, actually YES", want: ""},
		{name: "neither word", reply: "无法判定这道题", want: ""},
		{name: "empty reply", reply: "", want: ""},
		{name: "glued YESNO is not standalone", reply: "YESNO", want: ""},
		{name: "NOPE is not a standalone NO", reply: "NOPE", want: ""},
		{name: "YESES is not standalone", reply: "YESES", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, parseJudgeVerdict(tc.reply))
		})
	}
}

func TestTruncateJudgeCandidate(t *testing.T) {
	short := "答案是21"
	require.Equal(t, short, truncateJudgeCandidate(short))

	long := strings.Repeat("甲", PelicanJudgeCandidateMaxRunes+10)
	got := truncateJudgeCandidate(long)
	require.Equal(t, PelicanJudgeCandidateMaxRunes, len([]rune(got)))
	require.Equal(t, strings.Repeat("甲", PelicanJudgeCandidateMaxRunes), got)

	// ASCII multibyte-free input is capped by the same rune count.
	ascii := strings.Repeat("a", PelicanJudgeCandidateMaxRunes+1)
	require.Len(t, truncateJudgeCandidate(ascii), PelicanJudgeCandidateMaxRunes)
}

func TestJudgeNoteSnippet(t *testing.T) {
	require.Equal(t, "YES", judgeNoteSnippet("  YES \n"))
	require.Equal(t, "a b c", judgeNoteSnippet("a\n b\tc"))
	require.Equal(t, "", judgeNoteSnippet("   "))
	long := strings.Repeat("z", 300)
	require.LessOrEqual(t, len([]rune(judgeNoteSnippet(long))), 121)
}

func TestJudgeCandyAnswerValidation(t *testing.T) {
	svc := &PelicanProbeService{httpClient: &http.Client{Timeout: time.Second}}
	ctx := context.Background()

	_, _, err := svc.judgeCandyAnswer(ctx, "http://127.0.0.1:1", "k", "", "21")
	require.ErrorContains(t, err, "judge model not configured")

	_, _, err = svc.judgeCandyAnswer(ctx, "", "k", "judge", "21")
	require.ErrorContains(t, err, "judge base url not configured")

	_, _, err = svc.judgeCandyAnswer(ctx, "http://127.0.0.1:1", "", "judge", "21")
	require.ErrorContains(t, err, "judge api key not configured")
}

// --- fakes ---

type fakePelicanEncryptor struct{}

func (fakePelicanEncryptor) Encrypt(plaintext string) (string, error) {
	return "enc:" + plaintext, nil
}

func (fakePelicanEncryptor) Decrypt(_ string) (string, error) {
	return "test-probe-key", nil
}

type fakePelicanPlanRepo struct {
	byID    map[int64]*PelicanProbePlanRow
	created []*PelicanProbePlanRow
	nextID  int64
	ranIDs  []int64
}

func newFakePelicanPlanRepo() *fakePelicanPlanRepo {
	return &fakePelicanPlanRepo{byID: map[int64]*PelicanProbePlanRow{}, nextID: 1}
}

func (f *fakePelicanPlanRepo) Create(_ context.Context, plan *PelicanProbePlanRow) (*PelicanProbePlanRow, error) {
	cp := *plan
	cp.ID = f.nextID
	f.nextID++
	f.byID[cp.ID] = &cp
	f.created = append(f.created, &cp)
	return &cp, nil
}

func (f *fakePelicanPlanRepo) GetByID(_ context.Context, id int64) (*PelicanProbePlanRow, error) {
	if p, ok := f.byID[id]; ok {
		cp := *p
		return &cp, nil
	}
	return nil, context.DeadlineExceeded
}

func (f *fakePelicanPlanRepo) List(_ context.Context, _ *int64) ([]*PelicanProbePlanRow, error) {
	return nil, nil
}

func (f *fakePelicanPlanRepo) ListDue(_ context.Context, _ time.Time) ([]*PelicanProbePlanRow, error) {
	return nil, nil
}

func (f *fakePelicanPlanRepo) ListPublic(_ context.Context) ([]*PelicanProbePlanRow, error) {
	return nil, nil
}

func (f *fakePelicanPlanRepo) Update(_ context.Context, plan *PelicanProbePlanRow) (*PelicanProbePlanRow, error) {
	cp := *plan
	f.byID[cp.ID] = &cp
	return &cp, nil
}

func (f *fakePelicanPlanRepo) Delete(_ context.Context, _ int64) error { return nil }

func (f *fakePelicanPlanRepo) UpdateAfterRun(_ context.Context, id int64, _ time.Time, _ time.Time) error {
	f.ranIDs = append(f.ranIDs, id)
	return nil
}

type fakePelicanResultRepo struct {
	created []*PelicanProbeResult
	nextID  int64
	// honorCtxCancel mimics database/sql: cancelled contexts fail the query.
	honorCtxCancel bool
}

func (f *fakePelicanResultRepo) Create(ctx context.Context, result *PelicanProbeResult) (*PelicanProbeResult, error) {
	if f.honorCtxCancel && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	cp := *result
	f.nextID++
	cp.ID = f.nextID
	f.created = append(f.created, &cp)
	return &cp, nil
}

func (f *fakePelicanResultRepo) ListByPlanID(_ context.Context, _ int64, _ int) ([]*PelicanProbeResult, error) {
	return nil, nil
}

func (f *fakePelicanResultRepo) ListRecent(_ context.Context, _ *int64, _ int) ([]*PelicanProbeResult, error) {
	return nil, nil
}

func (f *fakePelicanResultRepo) ListPublicRecent(_ context.Context, _ int) ([]*PelicanProbeResult, error) {
	return nil, nil
}

func (f *fakePelicanResultRepo) Review(_ context.Context, _ int64, _, _, _ string) (*PelicanProbeResult, error) {
	return nil, nil
}

func (f *fakePelicanResultRepo) PruneOldResults(ctx context.Context, _ int64, _ int) error {
	if f.honorCtxCancel && ctx.Err() != nil {
		return ctx.Err()
	}
	return nil
}

func TestCreatePlanPelicanOptions(t *testing.T) {
	newSvc := func() (*PelicanProbeService, *fakePelicanPlanRepo) {
		plans := newFakePelicanPlanRepo()
		svc := &PelicanProbeService{planRepo: plans, encryptor: fakePelicanEncryptor{}}
		return svc, plans
	}
	base := CreatePlanInput{GroupID: 1, ModelID: "m", APIKey: "k"}

	t.Run("defaults to pelican", func(t *testing.T) {
		svc, plans := newSvc()
		got, err := svc.CreatePlan(context.Background(), base)
		require.NoError(t, err)
		require.Equal(t, PelicanTestTypePelican, got.TestType)
		require.Empty(t, got.ReasoningEffort)
		require.Equal(t, DefaultPelicanPrompt, plans.byID[got.ID].Prompt)
	})

	t.Run("candy forces fixed prompt", func(t *testing.T) {
		svc, plans := newSvc()
		in := base
		in.TestType = "candy"
		in.JudgeModelID = "judge-model"
		in.Prompt = "custom prompt that must be ignored"
		got, err := svc.CreatePlan(context.Background(), in)
		require.NoError(t, err)
		require.Equal(t, PelicanTestTypeCandy, got.TestType)
		require.Equal(t, DefaultCandyPrompt, plans.byID[got.ID].Prompt)
	})

	t.Run("invalid test type rejected", func(t *testing.T) {
		svc, _ := newSvc()
		in := base
		in.TestType = "vlm"
		_, err := svc.CreatePlan(context.Background(), in)
		require.ErrorContains(t, err, "invalid test_type")
	})

	t.Run("effort passthrough and validation", func(t *testing.T) {
		svc, _ := newSvc()
		in := base
		in.ReasoningEffort = "HIGH"
		got, err := svc.CreatePlan(context.Background(), in)
		require.NoError(t, err)
		require.Equal(t, "high", got.ReasoningEffort)

		in.ReasoningEffort = "bogus"
		_, err = svc.CreatePlan(context.Background(), in)
		require.ErrorContains(t, err, "invalid reasoning_effort")
	})
}

func TestCreatePlanJudgeModelValidation(t *testing.T) {
	newSvc := func() (*PelicanProbeService, *fakePelicanPlanRepo) {
		plans := newFakePelicanPlanRepo()
		svc := &PelicanProbeService{planRepo: plans, encryptor: fakePelicanEncryptor{}}
		return svc, plans
	}
	base := CreatePlanInput{GroupID: 1, ModelID: "m", APIKey: "k"}

	t.Run("candy without judge model accepted", func(t *testing.T) {
		// The ANSWER-line parser is the primary grader; the judge is an optional
		// fallback for answers that ignore the contract.
		svc, plans := newSvc()
		in := base
		in.TestType = "candy"
		got, err := svc.CreatePlan(context.Background(), in)
		require.NoError(t, err)
		require.Equal(t, PelicanTestTypeCandy, got.TestType)
		require.Empty(t, got.JudgeModelID)
		require.Equal(t, DefaultCandyPrompt, plans.byID[got.ID].Prompt)
	})

	t.Run("candy with blank judge model treated as unset", func(t *testing.T) {
		svc, _ := newSvc()
		in := base
		in.TestType = "candy"
		in.JudgeModelID = "   "
		got, err := svc.CreatePlan(context.Background(), in)
		require.NoError(t, err)
		require.Empty(t, got.JudgeModelID)
	})

	t.Run("candy with judge model accepted and trimmed", func(t *testing.T) {
		svc, plans := newSvc()
		in := base
		in.TestType = "candy"
		in.JudgeModelID = "  deepseek-chat  "
		got, err := svc.CreatePlan(context.Background(), in)
		require.NoError(t, err)
		require.Equal(t, PelicanTestTypeCandy, got.TestType)
		require.Equal(t, "deepseek-chat", got.JudgeModelID)
		require.Equal(t, "deepseek-chat", plans.byID[got.ID].JudgeModelID)
	})

	t.Run("pelican without judge model unaffected", func(t *testing.T) {
		svc, _ := newSvc()
		got, err := svc.CreatePlan(context.Background(), base)
		require.NoError(t, err)
		require.Equal(t, PelicanTestTypePelican, got.TestType)
		require.Empty(t, got.JudgeModelID)
	})

	t.Run("pelican with judge model is not validated", func(t *testing.T) {
		svc, _ := newSvc()
		in := base
		in.TestType = "pelican"
		in.JudgeModelID = "deepseek-chat"
		got, err := svc.CreatePlan(context.Background(), in)
		require.NoError(t, err)
		require.Equal(t, PelicanTestTypePelican, got.TestType)
	})
}

func pelicanStrPtr(s string) *string { return &s }

func pelicanBoolPtr(b bool) *bool { return &b }

func TestUpdatePlanPelicanOptions(t *testing.T) {
	newSvc := func(existing *PelicanProbePlanRow) (*PelicanProbeService, *fakePelicanPlanRepo) {
		plans := newFakePelicanPlanRepo()
		plans.byID[existing.ID] = existing
		svc := &PelicanProbeService{planRepo: plans, encryptor: fakePelicanEncryptor{}}
		return svc, plans
	}
	base := &PelicanProbePlanRow{
		PelicanProbePlan: PelicanProbePlan{
			ID: 7, GroupID: 1, ModelID: "m", TestType: PelicanTestTypePelican,
			CronExpression: "0 */4 * * *", Enabled: true, MaxResults: 50,
		},
		Prompt: DefaultPelicanPrompt,
	}

	t.Run("switch to candy pins fixed prompt", func(t *testing.T) {
		svc, plans := newSvc(base)
		got, err := svc.UpdatePlan(context.Background(), 7, UpdatePlanInput{
			TestType:     pelicanStrPtr("candy"),
			JudgeModelID: pelicanStrPtr("judge-m"),
			Prompt:       pelicanStrPtr("evil custom"),
		})
		require.NoError(t, err)
		require.Equal(t, PelicanTestTypeCandy, got.TestType)
		require.Equal(t, "judge-m", got.JudgeModelID)
		require.Equal(t, DefaultCandyPrompt, plans.byID[7].Prompt)
	})

	t.Run("switch to candy without judge model accepted", func(t *testing.T) {
		svc, plans := newSvc(base)
		got, err := svc.UpdatePlan(context.Background(), 7, UpdatePlanInput{TestType: pelicanStrPtr("candy")})
		require.NoError(t, err)
		require.Equal(t, PelicanTestTypeCandy, got.TestType)
		require.Empty(t, got.JudgeModelID)
		require.Equal(t, DefaultCandyPrompt, plans.byID[7].Prompt)
	})

	t.Run("candy judge model can be updated", func(t *testing.T) {
		candyBase := &PelicanProbePlanRow{
			PelicanProbePlan: PelicanProbePlan{
				ID: 7, GroupID: 1, ModelID: "m", TestType: PelicanTestTypeCandy,
				JudgeModelID: "old-judge", CronExpression: "0 */4 * * *", Enabled: true, MaxResults: 50,
			},
			Prompt: DefaultCandyPrompt,
		}
		svc, plans := newSvc(candyBase)
		got, err := svc.UpdatePlan(context.Background(), 7, UpdatePlanInput{JudgeModelID: pelicanStrPtr("new-judge")})
		require.NoError(t, err)
		require.Equal(t, "new-judge", got.JudgeModelID)
		require.Equal(t, "new-judge", plans.byID[7].JudgeModelID)

		got, err = svc.UpdatePlan(context.Background(), 7, UpdatePlanInput{JudgeModelID: pelicanStrPtr("  ")})
		require.NoError(t, err)
		require.Empty(t, got.JudgeModelID)
		require.Empty(t, plans.byID[7].JudgeModelID)
	})

	t.Run("pelican ignores judge model and keeps stored value", func(t *testing.T) {
		svc, plans := newSvc(base)
		got, err := svc.UpdatePlan(context.Background(), 7, UpdatePlanInput{JudgeModelID: pelicanStrPtr("ignored")})
		require.NoError(t, err)
		require.Empty(t, got.JudgeModelID)
		require.Empty(t, plans.byID[7].JudgeModelID)
	})

	t.Run("switch candy to pelican keeps stored judge model", func(t *testing.T) {
		candyBase := &PelicanProbePlanRow{
			PelicanProbePlan: PelicanProbePlan{
				ID: 7, GroupID: 1, ModelID: "m", TestType: PelicanTestTypeCandy,
				JudgeModelID: "kept-judge", CronExpression: "0 */4 * * *", Enabled: true, MaxResults: 50,
			},
			Prompt: DefaultCandyPrompt,
		}
		svc, plans := newSvc(candyBase)
		got, err := svc.UpdatePlan(context.Background(), 7, UpdatePlanInput{TestType: pelicanStrPtr("pelican")})
		require.NoError(t, err)
		require.Equal(t, PelicanTestTypePelican, got.TestType)
		require.Equal(t, "kept-judge", plans.byID[7].JudgeModelID)
	})

	t.Run("legacy candy plan without judge can still be toggled", func(t *testing.T) {
		legacy := &PelicanProbePlanRow{
			PelicanProbePlan: PelicanProbePlan{
				ID: 7, GroupID: 1, ModelID: "m", TestType: PelicanTestTypeCandy,
				CronExpression: "0 */4 * * *", Enabled: true, MaxResults: 50,
			},
			Prompt: DefaultCandyPrompt,
		}
		svc, _ := newSvc(legacy)
		got, err := svc.UpdatePlan(context.Background(), 7, UpdatePlanInput{Enabled: pelicanBoolPtr(false)})
		require.NoError(t, err)
		require.False(t, got.Enabled)
		require.Empty(t, got.JudgeModelID)
	})

	t.Run("invalid test type rejected", func(t *testing.T) {
		svc, _ := newSvc(base)
		_, err := svc.UpdatePlan(context.Background(), 7, UpdatePlanInput{TestType: pelicanStrPtr("nope")})
		require.ErrorContains(t, err, "invalid test_type")
	})

	t.Run("switch from candy resets pinned prompt", func(t *testing.T) {
		candyBase := &PelicanProbePlanRow{
			PelicanProbePlan: PelicanProbePlan{
				ID: 7, GroupID: 1, ModelID: "m", TestType: PelicanTestTypeCandy,
				CronExpression: "0 */4 * * *", Enabled: true, MaxResults: 50,
			},
			Prompt: DefaultCandyPrompt,
		}
		svc, plans := newSvc(candyBase)
		got, err := svc.UpdatePlan(context.Background(), 7, UpdatePlanInput{TestType: pelicanStrPtr("pelican")})
		require.NoError(t, err)
		require.Equal(t, PelicanTestTypePelican, got.TestType)
		require.Equal(t, DefaultPelicanPrompt, plans.byID[7].Prompt)

		svc2, plans2 := newSvc(candyBase)
		got, err = svc2.UpdatePlan(context.Background(), 7, UpdatePlanInput{
			TestType: pelicanStrPtr("pelican"),
			Prompt:   pelicanStrPtr("my custom"),
		})
		require.NoError(t, err)
		require.Equal(t, PelicanTestTypePelican, got.TestType)
		require.Equal(t, "my custom", plans2.byID[7].Prompt)
	})

	t.Run("effort update and clear", func(t *testing.T) {
		svc, plans := newSvc(base)
		got, err := svc.UpdatePlan(context.Background(), 7, UpdatePlanInput{ReasoningEffort: pelicanStrPtr("low")})
		require.NoError(t, err)
		require.Equal(t, "low", got.ReasoningEffort)
		require.Equal(t, "low", plans.byID[7].ReasoningEffort)

		got, err = svc.UpdatePlan(context.Background(), 7, UpdatePlanInput{ReasoningEffort: pelicanStrPtr("")})
		require.NoError(t, err)
		require.Empty(t, got.ReasoningEffort)

		_, err = svc.UpdatePlan(context.Background(), 7, UpdatePlanInput{ReasoningEffort: pelicanStrPtr("bogus")})
		require.ErrorContains(t, err, "invalid reasoning_effort")
	})
}

// probeTestServer returns a fake OpenAI-compatible endpoint that records the
// request body and replies with the given content (or HTTP 500 when fail).
func probeTestServer(t *testing.T, captured *map[string]any, content string, fail bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var decoded map[string]any
		_ = json.Unmarshal(body, &decoded)
		*captured = decoded
		if fail {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("boom"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		resp, _ := json.Marshal(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": content}}},
			"usage":   map[string]any{"prompt_tokens": 3, "completion_tokens": 5},
		})
		_, _ = w.Write(resp)
	}))
}

func probeSvcWithServer(t *testing.T, srv *httptest.Server) (*PelicanProbeService, *fakePelicanPlanRepo, *fakePelicanResultRepo) {
	t.Helper()
	plans := newFakePelicanPlanRepo()
	results := &fakePelicanResultRepo{honorCtxCancel: true}
	svc := &PelicanProbeService{
		planRepo: plans, resultRepo: results,
		encryptor:  fakePelicanEncryptor{},
		httpClient: srv.Client(),
	}
	return svc, plans, results
}

func probeRow(baseURL string) *PelicanProbePlanRow {
	return &PelicanProbePlanRow{
		PelicanProbePlan: PelicanProbePlan{
			ID: 1, GroupID: 1, ModelID: "m", BaseURL: baseURL,
			TestType: PelicanTestTypePelican, CronExpression: "0 */4 * * *",
			Enabled: true, MaxResults: 50,
		},
		Prompt:          "custom prompt",
		APIKeyEncrypted: "enc:k",
	}
}

func TestExecuteProbeRequestBody(t *testing.T) {
	t.Run("empty effort omitted", func(t *testing.T) {
		var captured map[string]any
		srv := probeTestServer(t, &captured, "<p>hi</p>", false)
		defer srv.Close()
		svc, _, _ := probeSvcWithServer(t, srv)

		row := probeRow(srv.URL)
		raw, pt, ct, _, err := svc.executeProbe(context.Background(), row)
		require.NoError(t, err)
		require.Equal(t, "<p>hi</p>", raw)
		require.Equal(t, 3, pt)
		require.Equal(t, 5, ct)
		_, hasEffort := captured["reasoning_effort"]
		require.False(t, hasEffort, "empty effort must not be sent")
		msgs, _ := captured["messages"].([]any)
		require.Len(t, msgs, 1)
		firstMsg, _ := msgs[0].(map[string]any)
		require.Equal(t, "custom prompt", firstMsg["content"])
	})

	t.Run("non-empty effort sent", func(t *testing.T) {
		var captured map[string]any
		srv := probeTestServer(t, &captured, "<p>hi</p>", false)
		defer srv.Close()
		svc, _, _ := probeSvcWithServer(t, srv)

		row := probeRow(srv.URL)
		row.ReasoningEffort = "medium"
		_, _, _, _, err := svc.executeProbe(context.Background(), row)
		require.NoError(t, err)
		require.Equal(t, "medium", captured["reasoning_effort"])
	})

	t.Run("candy uses fixed prompt", func(t *testing.T) {
		var captured map[string]any
		srv := probeTestServer(t, &captured, "21", false)
		defer srv.Close()
		svc, _, _ := probeSvcWithServer(t, srv)

		row := probeRow(srv.URL)
		row.TestType = PelicanTestTypeCandy
		_, _, _, _, err := svc.executeProbe(context.Background(), row)
		require.NoError(t, err)
		msgs, _ := captured["messages"].([]any)
		require.Len(t, msgs, 1)
		firstMsg, _ := msgs[0].(map[string]any)
		content, _ := firstMsg["content"].(string)
		require.Contains(t, content, "黑色的袋子")
	})
}

// recordedRequest captures one request seen by the fake gateway.
type recordedRequest struct {
	Path string
	Auth string
	Body map[string]any
}

// writeChatContent replies with a minimal OpenAI-compatible chat completion.
func writeChatContent(w http.ResponseWriter, content string) {
	w.Header().Set("Content-Type", "application/json")
	resp, _ := json.Marshal(map[string]any{
		"choices": []any{map[string]any{"message": map[string]any{"content": content}}},
		"usage":   map[string]any{"prompt_tokens": 3, "completion_tokens": 5},
	})
	_, _ = w.Write(resp)
}

// candyGateway fakes the probe endpoint and the judge endpoint on the same
// base URL. Requests whose model equals judgeModel are judge calls; every
// other request is treated as the probe call.
func candyGateway(
	t *testing.T,
	probeContent, judgeModel, judgeContent string,
	judgeStatus int,
	judgeDelay time.Duration,
) (*httptest.Server, *[]recordedRequest) {
	t.Helper()
	reqs := &[]recordedRequest{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var decoded map[string]any
		_ = json.Unmarshal(body, &decoded)
		*reqs = append(*reqs, recordedRequest{Path: r.URL.Path, Auth: r.Header.Get("Authorization"), Body: decoded})
		if model, _ := decoded["model"].(string); model == judgeModel && judgeModel != "" {
			if judgeDelay > 0 {
				time.Sleep(judgeDelay)
			}
			if judgeStatus >= 400 {
				w.WriteHeader(judgeStatus)
				_, _ = w.Write([]byte("judge boom"))
				return
			}
			writeChatContent(w, judgeContent)
			return
		}
		writeChatContent(w, probeContent)
	}))
	return srv, reqs
}

func candyJudgeRow(baseURL string) *PelicanProbePlanRow {
	return &PelicanProbePlanRow{
		PelicanProbePlan: PelicanProbePlan{
			ID: 1, GroupID: 1, ModelID: "probe-model", BaseURL: baseURL,
			TestType: PelicanTestTypeCandy, JudgeModelID: "judge-model",
			CronExpression: "0 */4 * * *", Enabled: true, MaxResults: 50,
		},
		Prompt:          DefaultCandyPrompt,
		APIKeyEncrypted: "enc:k",
	}
}

// runCandyJudge wires a fake gateway to a service and runs one candy probe.
func runCandyJudge(
	t *testing.T,
	row *PelicanProbePlanRow,
	probeContent, judgeModel, judgeContent string,
	judgeStatus int,
	judgeDelay time.Duration,
	timeout time.Duration,
) (*PelicanProbeResult, []recordedRequest) {
	t.Helper()
	srv, reqs := candyGateway(t, probeContent, judgeModel, judgeContent, judgeStatus, judgeDelay)
	defer srv.Close()
	client := srv.Client()
	if timeout > 0 {
		client.Timeout = timeout
	}
	svc := &PelicanProbeService{
		planRepo: newFakePelicanPlanRepo(), resultRepo: &fakePelicanResultRepo{honorCtxCancel: true},
		encryptor: fakePelicanEncryptor{}, httpClient: client,
	}
	row.BaseURL = srv.URL
	saved, err := svc.runRow(context.Background(), row, true)
	require.NoError(t, err)
	return saved, *reqs
}

func TestRunRowCandyJudgeVerdicts(t *testing.T) {
	t.Run("judge YES passes", func(t *testing.T) {
		saved, reqs := runCandyJudge(t, candyJudgeRow(""), "经过计算，最少需要21个糖果。", "judge-model", "YES", 0, 0, 0)
		require.Equal(t, PelicanRunSuccess, saved.Status)
		require.Equal(t, PelicanVerdictPass, saved.Verdict)
		require.Equal(t, PelicanAutoReviewer, saved.ReviewedBy)
		require.NotNil(t, saved.ReviewedAt)
		require.Contains(t, saved.Note, "judge=judge-model: YES")
		require.Len(t, reqs, 2, "candy runs must cost exactly two gateway calls")
	})

	t.Run("judge NO degrades", func(t *testing.T) {
		saved, reqs := runCandyJudge(t, candyJudgeRow(""), "答案是 20。", "judge-model", "NO", 0, 0, 0)
		require.Equal(t, PelicanRunSuccess, saved.Status)
		require.Equal(t, PelicanVerdictDegraded, saved.Verdict)
		require.Equal(t, PelicanAutoReviewer, saved.ReviewedBy)
		require.Contains(t, saved.Note, "judge=judge-model: NO")
		require.Len(t, reqs, 2)
	})

	t.Run("lowercase verdict accepted", func(t *testing.T) {
		saved, _ := runCandyJudge(t, candyJudgeRow(""), "21", "judge-model", "yes", 0, 0, 0)
		require.Equal(t, PelicanVerdictPass, saved.Verdict)
		require.Contains(t, saved.Note, "judge=judge-model: YES")
	})

	t.Run("ambiguous reply stays pending", func(t *testing.T) {
		saved, _ := runCandyJudge(t, candyJudgeRow(""), "21", "judge-model", "YES and NO", 0, 0, 0)
		require.Equal(t, PelicanRunSuccess, saved.Status)
		require.Equal(t, PelicanVerdictPending, saved.Verdict)
		require.Empty(t, saved.ReviewedBy)
		require.Nil(t, saved.ReviewedAt)
		require.Contains(t, saved.Note, "auto-judge skipped")
	})

	t.Run("off-topic reply stays pending", func(t *testing.T) {
		saved, _ := runCandyJudge(t, candyJudgeRow(""), "21", "judge-model", "我无法判断", 0, 0, 0)
		require.Equal(t, PelicanVerdictPending, saved.Verdict)
		require.Contains(t, saved.Note, "auto-judge skipped")
	})
}

func TestRunRowCandyJudgeRequestShape(t *testing.T) {
	candidate := "推理过程……最终答案 21"
	saved, reqs := runCandyJudge(t, candyJudgeRow(""), candidate, "judge-model", "YES", 0, 0, 0)
	require.Equal(t, PelicanVerdictPass, saved.Verdict)
	require.Len(t, reqs, 2)

	probe, judge := reqs[0], reqs[1]
	require.Equal(t, "/v1/chat/completions", probe.Path)
	require.Equal(t, "/v1/chat/completions", judge.Path)
	require.Equal(t, "Bearer test-probe-key", probe.Auth)
	require.Equal(t, "Bearer test-probe-key", judge.Auth, "judge must reuse the probe key")

	// Probe request keeps the candy fixed prompt and probe model.
	require.Equal(t, "probe-model", probe.Body["model"])
	probeMsgs, _ := probe.Body["messages"].([]any)
	require.Len(t, probeMsgs, 1)
	probeMsg, _ := probeMsgs[0].(map[string]any)
	require.Equal(t, DefaultCandyPrompt, probeMsg["content"])

	// Judge request: judge model, deterministic sampling, no token cap.
	require.Equal(t, "judge-model", judge.Body["model"])
	require.Equal(t, false, judge.Body["stream"])
	require.Equal(t, float64(0), judge.Body["temperature"])
	for _, banned := range []string{"max_tokens", "max_completion_tokens", "max_output_tokens", "reasoning_effort"} {
		_, has := judge.Body[banned]
		require.Falsef(t, has, "judge request must not send %s", banned)
	}
	judgeMsgs, _ := judge.Body["messages"].([]any)
	require.Len(t, judgeMsgs, 1)
	judgeMsg, _ := judgeMsgs[0].(map[string]any)
	judgePrompt, _ := judgeMsg["content"].(string)
	require.Contains(t, judgePrompt, "严格的判分员")
	require.Contains(t, judgePrompt, candidate)
	require.True(t, strings.HasPrefix(judgePrompt, "你是一个严格的判分员。"))
	require.True(t, strings.HasSuffix(judgePrompt, "---"))
}

func TestRunRowCandyJudgePromptTruncatesCandidate(t *testing.T) {
	long := strings.Repeat("甲", PelicanJudgeCandidateMaxRunes+50)
	saved, reqs := runCandyJudge(t, candyJudgeRow(""), long, "judge-model", "NO", 0, 0, 0)
	require.Equal(t, PelicanVerdictDegraded, saved.Verdict)
	require.Len(t, reqs, 2)
	msgs, _ := reqs[1].Body["messages"].([]any)
	msg, _ := msgs[0].(map[string]any)
	prompt, _ := msg["content"].(string)
	require.Contains(t, prompt, strings.Repeat("甲", PelicanJudgeCandidateMaxRunes))
	require.NotContains(t, prompt, strings.Repeat("甲", PelicanJudgeCandidateMaxRunes+1))
}

func TestRunRowCandyJudgeUnavailable(t *testing.T) {
	t.Run("judge non-2xx keeps probe success and pending", func(t *testing.T) {
		saved, reqs := runCandyJudge(t, candyJudgeRow(""), "21", "judge-model", "", http.StatusInternalServerError, 0, 0)
		require.Equal(t, PelicanRunSuccess, saved.Status, "probe succeeded; only the judge failed")
		require.Equal(t, PelicanVerdictPending, saved.Verdict)
		require.Empty(t, saved.ReviewedBy)
		require.Nil(t, saved.ReviewedAt)
		require.Contains(t, saved.Note, "auto-judge skipped")
		require.Contains(t, saved.Note, "500")
		require.Len(t, reqs, 2)
	})

	t.Run("judge timeout keeps probe success and pending", func(t *testing.T) {
		saved, reqs := runCandyJudge(t, candyJudgeRow(""), "21", "judge-model", "YES", 0, 400*time.Millisecond, 60*time.Millisecond)
		require.Equal(t, PelicanRunSuccess, saved.Status)
		require.Equal(t, PelicanVerdictPending, saved.Verdict)
		require.Empty(t, saved.ReviewedBy)
		require.Contains(t, saved.Note, "auto-judge skipped")
		require.Len(t, reqs, 2)
	})

	t.Run("judge empty content keeps probe success and pending", func(t *testing.T) {
		saved, _ := runCandyJudge(t, candyJudgeRow(""), "21", "judge-model", "", 0, 0, 0)
		require.Equal(t, PelicanRunSuccess, saved.Status)
		require.Equal(t, PelicanVerdictPending, saved.Verdict)
		require.Contains(t, saved.Note, "auto-judge skipped")
		require.Contains(t, saved.Note, "empty judge content")
	})
}

func TestRunRowCandyJudgeFallback(t *testing.T) {
	t.Run("legacy plan without judge model skips judging", func(t *testing.T) {
		row := candyJudgeRow("")
		row.JudgeModelID = ""
		saved, reqs := runCandyJudge(t, row, "21", "judge-model", "YES", 0, 0, 0)
		require.Equal(t, PelicanRunSuccess, saved.Status)
		require.Equal(t, PelicanVerdictPending, saved.Verdict)
		require.Empty(t, saved.ReviewedBy)
		require.Nil(t, saved.ReviewedAt)
		require.Contains(t, saved.Note, "auto-judge skipped")
		require.Contains(t, saved.Note, "judge_model_id not configured")
		require.Len(t, reqs, 1, "no judge call may be attempted without a judge model")
	})

	t.Run("probe failure skips the judge", func(t *testing.T) {
		// A judge model that never matches keeps every request on the probe path.
		saved, reqs := runCandyJudge(t, candyJudgeRow(""), "", "judge-model", "YES", 0, 0, 0)
		require.Equal(t, PelicanRunFailed, saved.Status)
		// Not pending: a probe that returned nothing has no answer to judge, so it
		// must not sit in the review queue waiting for a pass/degraded click.
		require.Equal(t, PelicanVerdictInvalid, saved.Verdict)
		require.Empty(t, saved.ReviewedBy)
		require.Nil(t, saved.ReviewedAt)
		require.Len(t, reqs, 1, "a failed probe must not reach the judge")
	})

	t.Run("pelican plan never calls the judge", func(t *testing.T) {
		row := candyJudgeRow("")
		row.TestType = PelicanTestTypePelican
		saved, reqs := runCandyJudge(t, row, "<svg/>21", "judge-model", "YES", 0, 0, 0)
		require.Equal(t, PelicanRunSuccess, saved.Status)
		require.Equal(t, PelicanVerdictPending, saved.Verdict)
		require.Empty(t, saved.ReviewedBy)
		require.Len(t, reqs, 1)
	})
}

func TestRunRowPersistsAfterRequestCancel(t *testing.T) {
	var captured map[string]any
	srv := probeTestServer(t, &captured, "<p>hi</p>", false)
	defer srv.Close()
	svc, plans, results := probeSvcWithServer(t, srv)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // simulate frontend 30s timeout cutting the request
	saved, err := svc.runRow(ctx, probeRow(srv.URL), true)
	// executeProbe honors cancellation (upstream call fails), but persistence
	// uses a detached context so the failed result still lands in the DB.
	require.NoError(t, err)
	require.NotNil(t, saved)
	require.Equal(t, PelicanRunFailed, saved.Status)
	require.Len(t, results.created, 1)
	require.Equal(t, []int64{1}, plans.ranIDs)
}

// A probe that failed at the transport layer produced no model output at all,
// so there is nothing a reviewer could judge. It must carry invalid instead of
// the default pending: a pending row lands in the review queue next to the
// pass/degraded buttons, and one click records an upstream rate limit as a
// quality verdict.
func TestRunRowMarksFailedProbeInvalid(t *testing.T) {
	t.Run("HTTP 5xx marks the run invalid", func(t *testing.T) {
		var captured map[string]any
		srv := probeTestServer(t, &captured, "", true)
		defer srv.Close()
		svc, _, _ := probeSvcWithServer(t, srv)

		saved, err := svc.runRow(context.Background(), probeRow(srv.URL), true)
		require.NoError(t, err)
		require.Equal(t, PelicanRunFailed, saved.Status)
		require.Equal(t, PelicanVerdictInvalid, saved.Verdict)
		require.NotEqual(t, PelicanVerdictPending, saved.Verdict,
			"a failed probe must not enter the pending-review queue")
		require.NotEmpty(t, saved.ErrorMessage)
		require.Empty(t, saved.ReviewedBy)
		require.Nil(t, saved.ReviewedAt)
	})

	t.Run("cancelled context marks the run invalid", func(t *testing.T) {
		var captured map[string]any
		srv := probeTestServer(t, &captured, "<p>hi</p>", false)
		defer srv.Close()
		svc, _, _ := probeSvcWithServer(t, srv)

		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		saved, err := svc.runRow(ctx, probeRow(srv.URL), true)
		require.NoError(t, err)
		require.Equal(t, PelicanRunFailed, saved.Status)
		require.Equal(t, PelicanVerdictInvalid, saved.Verdict)
	})

	// Guard against over-correcting: a run that actually produced output still
	// needs a human verdict, so its verdict must stay pending.
	t.Run("a successful run still awaits review", func(t *testing.T) {
		var captured map[string]any
		srv := probeTestServer(t, &captured, "<p>hi</p>", false)
		defer srv.Close()
		svc, _, _ := probeSvcWithServer(t, srv)

		saved, err := svc.runRow(context.Background(), probeRow(srv.URL), true)
		require.NoError(t, err)
		require.Equal(t, PelicanRunSuccess, saved.Status)
		require.Equal(t, PelicanVerdictPending, saved.Verdict,
			"a real result still needs a human verdict")
	})
}
