package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStripCodeFences(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{name: "plain html", in: `<html><body>hi</body></html>`, want: `<html><body>hi</body></html>`},
		{name: "fenced html", in: "```html\n<html></html>\n```", want: `<html></html>`},
		{name: "bare fence", in: "```\n<div/>\n```", want: `<div/>`},
		{name: "surrounding whitespace", in: "  ```html\n<a/>\n```  \n", want: `<a/>`},
		{name: "no closing fence keeps body", in: "```html\n<a/>", want: `<a/>`},
		{name: "inline code untouched", in: "use `code` here", want: "use `code` here"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, stripCodeFences(tc.in))
		})
	}
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
