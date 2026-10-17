package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseCandyAnswer(t *testing.T) {
	cases := []struct {
		name     string
		content  string
		answer   int
		found    bool
		conflict bool
	}{
		{name: "canonical", content: "推导……\nANSWER: 21", answer: 21, found: true},
		{name: "full width colon", content: "ANSWER：21", answer: 21, found: true},
		{name: "bold", content: "**ANSWER: 21**", answer: 21, found: true},
		{name: "underscore emphasis", content: "__ANSWER: 21__", answer: 21, found: true},
		{name: "blockquote prefix", content: "> ANSWER: 21", answer: 21, found: true},
		{name: "list prefix", content: "- ANSWER: 21", answer: 21, found: true},
		{name: "lowercase", content: "answer: 21", answer: 21, found: true},
		{name: "trailing unit", content: "ANSWER: 21 个", answer: 21, found: true},
		{name: "trailing punctuation", content: "ANSWER: 21。", answer: 21, found: true},
		{name: "wrong answer", content: "ANSWER: 29", answer: 29, found: true},
		{name: "three digits", content: "ANSWER: 104", answer: 104, found: true},
		{name: "repeated same value", content: "ANSWER: 21\n\nANSWER: 21", answer: 21, found: true},
		{name: "conflicting lines", content: "ANSWER: 21\nANSWER: 29", found: true, conflict: true},
		{name: "conflicting with repeat", content: "ANSWER: 21\nANSWER: 29\nANSWER: 21", found: true, conflict: true},
		{name: "placeholder ignored", content: "最后单独一行只输出 ANSWER: <数字>", found: false},
		{name: "prose only", content: "经过计算，最少需要 21 个糖果。", found: false},
		{name: "empty", content: "", found: false},
		{name: "boxed only", content: `\[ \boxed{21} \]`, found: false},
		{name: "spaced digits", content: "ANSWER: 2 1", found: false},
		{name: "chinese label", content: "答案: 21", found: false},
		{name: "suffix text", content: "ANSWER: 21 个糖果", found: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseCandyAnswer(tc.content)
			require.Equal(t, tc.found, got.Found, "found")
			require.Equal(t, tc.conflict, got.Conflict, "conflict")
			if tc.found && !tc.conflict {
				require.Equal(t, tc.answer, got.Answer)
			}
		})
	}
}

func TestCandyPromptAnswerContract(t *testing.T) {
	require.Equal(t, 21, PelicanCandyExpectedAnswer)
	// The output contract the deterministic parser relies on.
	require.Contains(t, DefaultCandyPrompt, "ANSWER: 数字")
	// The problem statement must stay verbatim: no hint that the shapes can be
	// chosen by touch. Spelling that out would answer the reading-comprehension
	// part of the test for the model.
	require.NotContains(t, DefaultCandyPrompt, "按手感选择")
	require.NotContains(t, DefaultCandyPrompt, "调整后续选择")
	// The original parenthetical is intact.
	require.Contains(t, DefaultCandyPrompt, "或者有圆形桃子味匹配五角星苹果味糖果都满足要求）")
}

func TestApplyCandyVerdict(t *testing.T) {
	row := func(judgeModel string) *PelicanProbePlanRow {
		return &PelicanProbePlanRow{PelicanProbePlan: PelicanProbePlan{
			ID: 1, GroupID: 1, ModelID: "probe-model",
			TestType: PelicanTestTypeCandy, JudgeModelID: judgeModel,
		}}
	}
	newResult := func() *PelicanProbeResult {
		return &PelicanProbeResult{PlanID: 1, Status: PelicanRunSuccess, Verdict: PelicanVerdictPending}
	}

	t.Run("expected answer passes without a judge call", func(t *testing.T) {
		svc := &PelicanProbeService{}
		res := newResult()
		svc.applyCandyVerdict(context.Background(), row("judge-model"), res, "推导……\nANSWER: 21")
		require.Equal(t, PelicanVerdictPass, res.Verdict)
		require.Equal(t, PelicanAutoReviewer, res.ReviewedBy)
		require.NotNil(t, res.ReviewedAt)
		require.NotNil(t, res.AnswerValue)
		require.Equal(t, 21, *res.AnswerValue)
		require.Contains(t, res.Note, "auto-parse: answer=21 expected=21")
	})

	t.Run("other answer degrades and records it", func(t *testing.T) {
		svc := &PelicanProbeService{}
		res := newResult()
		svc.applyCandyVerdict(context.Background(), row("judge-model"), res, "盲摸读法：\nANSWER: 29")
		require.Equal(t, PelicanVerdictDegraded, res.Verdict)
		require.NotNil(t, res.AnswerValue)
		require.Equal(t, 29, *res.AnswerValue)
		require.Contains(t, res.Note, "answer=29 expected=21")
	})

	t.Run("conflicting answer lines stay pending without a judge", func(t *testing.T) {
		svc := &PelicanProbeService{}
		res := newResult()
		svc.applyCandyVerdict(context.Background(), row(""), res, "ANSWER: 21\nANSWER: 29")
		require.Equal(t, PelicanVerdictPending, res.Verdict)
		require.Nil(t, res.AnswerValue)
		require.Empty(t, res.ReviewedBy)
		require.Nil(t, res.ReviewedAt)
		require.Contains(t, res.Note, "conflicting ANSWER lines")
		require.Contains(t, res.Note, "judge_model_id not configured")
	})
}

func TestRunRowCandyAnswerLine(t *testing.T) {
	t.Run("answer line is graded without the judge", func(t *testing.T) {
		saved, reqs := runCandyJudge(t, candyJudgeRow(""), "推导……\nANSWER: 21", "judge-model", "YES", 0, 0, 0)
		require.Equal(t, PelicanRunSuccess, saved.Status)
		require.Equal(t, PelicanVerdictPass, saved.Verdict)
		require.Equal(t, PelicanAutoReviewer, saved.ReviewedBy)
		require.NotNil(t, saved.AnswerValue)
		require.Equal(t, 21, *saved.AnswerValue)
		require.Contains(t, saved.Note, "auto-parse")
		require.Len(t, reqs, 1, "a parsed answer must not cost a judge call")
	})

	t.Run("wrong answer line degrades without the judge", func(t *testing.T) {
		saved, reqs := runCandyJudge(t, candyJudgeRow(""), "盲摸读法：\nANSWER: 29", "judge-model", "YES", 0, 0, 0)
		require.Equal(t, PelicanVerdictDegraded, saved.Verdict)
		require.NotNil(t, saved.AnswerValue)
		require.Equal(t, 29, *saved.AnswerValue)
		require.Len(t, reqs, 1)
	})

	t.Run("missing answer line falls back to the judge", func(t *testing.T) {
		saved, reqs := runCandyJudge(t, candyJudgeRow(""), "经过计算，答案是 21。", "judge-model", "YES", 0, 0, 0)
		require.Equal(t, PelicanVerdictPass, saved.Verdict)
		require.Nil(t, saved.AnswerValue)
		require.Contains(t, saved.Note, "judge=judge-model: YES")
		require.Contains(t, saved.Note, "auto-parse: no ANSWER line")
		require.Len(t, reqs, 2)
	})

	t.Run("conflicting answer lines fall back to the judge", func(t *testing.T) {
		saved, reqs := runCandyJudge(t, candyJudgeRow(""), "ANSWER: 21\nANSWER: 29", "judge-model", "NO", 0, 0, 0)
		require.Equal(t, PelicanVerdictDegraded, saved.Verdict)
		require.Nil(t, saved.AnswerValue)
		require.Contains(t, saved.Note, "auto-parse: conflicting ANSWER lines")
		require.Len(t, reqs, 2)
	})
}
