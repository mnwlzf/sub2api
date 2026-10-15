package service

import (
	"context"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// A replayed reasoning block minted by another exit/caller: id + summary + blob.
const openAIEncryptedReasoningLadderTestBody = `{"model":"gpt-5","stream":false,"input":[
	{"type":"reasoning","id":"rs_6ad05224","status":"completed","summary":[{"type":"summary_text","text":"keep me"}],"encrypted_content":"other-account-blob"},
	{"type":"message","content":[{"type":"input_text","text":"hi"}]}]}`

// The second 400 of the same family: dropping encrypted_content while keeping the
// id leaves a dangling reference the upstream cannot resolve.
func openAIEncryptedReasoningNotFoundBody(reasoningID string) string {
	return `{"error":{"code":null,"message":"Referenced reasoning item '` + reasoningID + `' was not found or has expired.","param":null,"type":"invalid_request_error"}}`
}

// Stage 1 is enough: the upstream accepts the id-preserving, blob-free body.
func TestOpenAIGatewayService_Forward_EncryptedReasoningLadderStopsAtStageOne(t *testing.T) {
	gin.SetMode(gin.TestMode)
	sink, release := captureStructuredLog(t)
	defer release()

	svc, upstream, account := newOpenAIEncryptedContentRetryTestUpstream(
		openAIEncryptedContentRetryTestResponse(http.StatusBadRequest, openAIEncryptedContentNotIssuedBody),
		openAIEncryptedContentRetryTestResponse(http.StatusOK, `{"id":"resp_recovered","usage":{"input_tokens":1,"output_tokens":2}}`),
	)

	result, err := svc.Forward(context.Background(), newOpenAIEncryptedContentRetryTestContext(), account, []byte(openAIEncryptedReasoningLadderTestBody))
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, upstream.bodies, 2, "stage one must stop the ladder")

	require.Equal(t, "other-account-blob", gjson.GetBytes(upstream.bodies[0], "input.0.encrypted_content").String())
	require.False(t, gjson.GetBytes(upstream.bodies[1], "input.0.encrypted_content").Exists())
	require.Equal(t, "rs_6ad05224", gjson.GetBytes(upstream.bodies[1], "input.0.id").String(), "stage one keeps the id")
	require.Equal(t, "keep me", gjson.GetBytes(upstream.bodies[1], "input.0.summary.0.text").String())
	require.Equal(t, "hi", gjson.GetBytes(upstream.bodies[1], "input.1.content.0.text").String())
	require.True(t, sink.ContainsMessage("escalating to stage=1 action=drop_encrypted_content"),
		"the rung actually taken must be observable in the logs")
}

// Stage 1 fails with the dangling-reference message, so the ladder deepens to
// stage 2 (drop the id) and the upstream accepts.
func TestOpenAIGatewayService_Forward_EncryptedReasoningLadderEscalatesToStageTwo(t *testing.T) {
	gin.SetMode(gin.TestMode)
	sink, release := captureStructuredLog(t)
	defer release()

	svc, upstream, account := newOpenAIEncryptedContentRetryTestUpstream(
		openAIEncryptedContentRetryTestResponse(http.StatusBadRequest, openAIEncryptedContentNotIssuedBody),
		openAIEncryptedContentRetryTestResponse(http.StatusBadRequest, openAIEncryptedReasoningNotFoundBody("rs_6ad05224")),
		openAIEncryptedContentRetryTestResponse(http.StatusOK, `{"id":"resp_recovered","usage":{"input_tokens":1,"output_tokens":2}}`),
	)

	result, err := svc.Forward(context.Background(), newOpenAIEncryptedContentRetryTestContext(), account, []byte(openAIEncryptedReasoningLadderTestBody))
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, upstream.bodies, 3, "stage two must be reached after the not-found rejection")

	require.False(t, gjson.GetBytes(upstream.bodies[1], "input.0.encrypted_content").Exists())
	require.Equal(t, "rs_6ad05224", gjson.GetBytes(upstream.bodies[1], "input.0.id").String())
	require.False(t, gjson.GetBytes(upstream.bodies[2], "input.0.encrypted_content").Exists())
	require.False(t, gjson.GetBytes(upstream.bodies[2], "input.0.id").Exists(), "stage two drops the dangling id")
	require.Equal(t, "reasoning", gjson.GetBytes(upstream.bodies[2], "input.0.type").String(), "the placeholder item survives")
	require.Equal(t, "keep me", gjson.GetBytes(upstream.bodies[2], "input.0.summary.0.text").String())
	require.Equal(t, "hi", gjson.GetBytes(upstream.bodies[2], "input.1.content.0.text").String())
	require.True(t, sink.ContainsMessage("escalating to stage=2 action=drop_reasoning_id"),
		"the rung actually taken must be observable in the logs")
}

// Stages 1 and 2 both fail, so stage 3 drops the reasoning item entirely.
func TestOpenAIGatewayService_Forward_EncryptedReasoningLadderEscalatesToStageThree(t *testing.T) {
	gin.SetMode(gin.TestMode)
	sink, release := captureStructuredLog(t)
	defer release()

	svc, upstream, account := newOpenAIEncryptedContentRetryTestUpstream(
		openAIEncryptedContentRetryTestResponse(http.StatusBadRequest, openAIEncryptedContentNotIssuedBody),
		openAIEncryptedContentRetryTestResponse(http.StatusBadRequest, openAIEncryptedReasoningNotFoundBody("rs_6ad05224")),
		openAIEncryptedContentRetryTestResponse(http.StatusBadRequest, openAIEncryptedContentNotIssuedBody),
		openAIEncryptedContentRetryTestResponse(http.StatusOK, `{"id":"resp_recovered","usage":{"input_tokens":1,"output_tokens":2}}`),
	)

	result, err := svc.Forward(context.Background(), newOpenAIEncryptedContentRetryTestContext(), account, []byte(openAIEncryptedReasoningLadderTestBody))
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, upstream.bodies, 4, "stage three must be reached after two failed rungs")

	require.Equal(t, "message", gjson.GetBytes(upstream.bodies[3], "input.0.type").String(), "stage three drops the reasoning item")
	require.False(t, gjson.GetBytes(upstream.bodies[3], "input.1").Exists())
	require.Equal(t, "hi", gjson.GetBytes(upstream.bodies[3], "input.0.content.0.text").String())
	require.True(t, sink.ContainsMessage("escalating to stage=3 action=drop_reasoning_item"),
		"the rung actually taken must be observable in the logs")
}

// All three rungs fail: the ladder gives up, returns the upstream error, and never
// retries a rung twice.
func TestOpenAIGatewayService_Forward_EncryptedReasoningLadderGivesUpAfterStageThree(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc, upstream, account := newOpenAIEncryptedContentRetryTestUpstream(
		openAIEncryptedContentRetryTestResponse(http.StatusBadRequest, openAIEncryptedContentNotIssuedBody),
		openAIEncryptedContentRetryTestResponse(http.StatusBadRequest, openAIEncryptedReasoningNotFoundBody("rs_6ad05224")),
		openAIEncryptedContentRetryTestResponse(http.StatusBadRequest, openAIEncryptedContentNotIssuedBody),
		openAIEncryptedContentRetryTestResponse(http.StatusBadRequest, openAIEncryptedReasoningNotFoundBody("rs_6ad05224")),
		openAIEncryptedContentRetryTestResponse(http.StatusOK, `{"id":"resp_unexpected","usage":{"input_tokens":1,"output_tokens":2}}`),
	)

	_, err := svc.Forward(context.Background(), newOpenAIEncryptedContentRetryTestContext(), account, []byte(openAIEncryptedReasoningLadderTestBody))
	require.Error(t, err)
	require.Len(t, upstream.bodies, 4, "the ladder is capped at three retries (four upstream attempts)")
}

// A not-found rejection on a body that already lost its encrypted_content (the
// lineage pre-strip does exactly that) skips stage one and goes straight to stage
// two rather than spending a retry on a body that cannot change.
func TestOpenAIGatewayService_Forward_EncryptedReasoningLadderSkipsStageOneWhenBlobAlreadyGone(t *testing.T) {
	gin.SetMode(gin.TestMode)
	sink, release := captureStructuredLog(t)
	defer release()

	svc, upstream, account := newOpenAIEncryptedContentRetryTestUpstream(
		openAIEncryptedContentRetryTestResponse(http.StatusBadRequest, openAIEncryptedReasoningNotFoundBody("rs_6ad05224")),
		openAIEncryptedContentRetryTestResponse(http.StatusOK, `{"id":"resp_recovered","usage":{"input_tokens":1,"output_tokens":2}}`),
	)
	body := []byte(`{"model":"gpt-5","stream":false,"input":[
		{"type":"reasoning","id":"rs_6ad05224","status":"completed","summary":[{"type":"summary_text","text":"keep me"}]},
		{"type":"message","content":[{"type":"input_text","text":"hi"}]}]}`)

	result, err := svc.Forward(context.Background(), newOpenAIEncryptedContentRetryTestContext(), account, body)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, upstream.bodies, 2, "the skipped rung must not cost an upstream request")
	require.False(t, gjson.GetBytes(upstream.bodies[1], "input.0.id").Exists())
	require.Equal(t, "keep me", gjson.GetBytes(upstream.bodies[1], "input.0.summary.0.text").String())
	require.True(t, sink.ContainsMessage("Skip non-WSv2 invalid_encrypted_content retry stage=1 action=drop_encrypted_content"),
		"the skipped rung must be observable in the logs")
	require.False(t, sink.ContainsMessage("Skip non-WSv2 invalid_encrypted_content retry because encrypted reasoning items are missing"),
		"a skipped rung that still lets the ladder act is not a dead end")
}

// The not-found matcher must stay narrow and the ladder must not fire on anything
// else, and a request with no reasoning items must not retry at all.
func TestOpenAIGatewayService_Forward_EncryptedReasoningLadderDoesNotOverTrigger(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name     string
		status   int
		upstream string
		input    string
	}{
		{
			name:     "unrelated 400",
			status:   http.StatusBadRequest,
			upstream: `{"error":{"code":null,"message":"bad request","type":"invalid_request_error"}}`,
		},
		{
			name:     "not found without the reasoning-item prefix",
			status:   http.StatusBadRequest,
			upstream: `{"error":{"code":null,"message":"the previous response was not found or has expired","type":"invalid_request_error"}}`,
		},
		{
			name:     "reasoning item prefix without the expiry clause",
			status:   http.StatusBadRequest,
			upstream: `{"error":{"code":null,"message":"Referenced reasoning item 'rs_1' is malformed","type":"invalid_request_error"}}`,
		},
		{
			name:     "not-found message but not a 400",
			status:   http.StatusInternalServerError,
			upstream: openAIEncryptedReasoningNotFoundBody("rs_6ad05224"),
		},
		{
			name:     "no reasoning items to act on",
			status:   http.StatusBadRequest,
			upstream: openAIEncryptedContentNotIssuedBody,
			input:    `[{"type":"message","content":[{"type":"input_text","text":"hi"}]}]`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, upstream, account := newOpenAIEncryptedContentRetryTestUpstream(
				openAIEncryptedContentRetryTestResponse(tc.status, tc.upstream),
			)
			input := tc.input
			if input == "" {
				input = `[
					{"type":"reasoning","id":"rs_6ad05224","status":"completed","summary":[{"type":"summary_text","text":"keep me"}],"encrypted_content":"other-account-blob"},
					{"type":"message","content":[{"type":"input_text","text":"hi"}]}]`
			}
			_, _ = svc.Forward(context.Background(), newOpenAIEncryptedContentRetryTestContext(), account,
				[]byte(`{"model":"gpt-5","stream":false,"input":`+input+`}`))
			require.Len(t, upstream.bodies, 1, "the ladder must not trigger")
		})
	}
}
