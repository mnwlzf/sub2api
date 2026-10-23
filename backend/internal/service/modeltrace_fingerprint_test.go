package service

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestModelTraceGPTReferenceParity(t *testing.T) {
	raw, err := os.ReadFile("testdata/modeltrace_gpt_reference.json")
	require.NoError(t, err)
	var samples []struct {
		Model         string `json:"model"`
		ExpectedCount int    `json:"expected_count"`
		ParsedCount   int    `json:"parsed_count"`
		Text          string `json:"text"`
	}
	require.NoError(t, json.Unmarshal(raw, &samples))
	require.Len(t, samples, 8)
	for _, sample := range samples {
		t.Run(sample.Model, func(t *testing.T) {
			prediction, commit, err := ModelTracePredictCommitted(sample.Text, sample.ExpectedCount)
			require.NoError(t, err)
			require.Equal(t, sample.Model, prediction.Model)
			require.Equal(t, sample.ParsedCount, prediction.ParsedCount)
			require.Greater(t, prediction.Probability, 0.97)
			require.Equal(t, modelTraceFallbackCommit, commit)
		})
	}
}

func TestModelTraceRejectsTruncatedChallenge(t *testing.T) {
	prediction, _, err := ModelTracePredictCommitted("1 2 3 4 5", 332)
	require.ErrorContains(t, err, "insufficient_numbers")
	require.Equal(t, 5, prediction.ParsedCount)
}

func TestModelTraceBankRejectsInvalidData(t *testing.T) {
	_, err := parseModelTraceBank([]byte(`{"schema":"robust-number-fingerprint-bank","models":[{"id":"gpt-1"}]}`))
	require.Error(t, err)
}

func referenceSampleText(t *testing.T, model string) (string, int) {
	t.Helper()
	raw, err := os.ReadFile("testdata/modeltrace_gpt_reference.json")
	require.NoError(t, err)
	var samples []struct {
		Model         string `json:"model"`
		ExpectedCount int    `json:"expected_count"`
		Text          string `json:"text"`
	}
	require.NoError(t, json.Unmarshal(raw, &samples))
	for _, sample := range samples {
		if sample.Model == model {
			return sample.Text, sample.ExpectedCount
		}
	}
	t.Fatalf("no reference sample for %s", model)
	return "", 0
}

func duplicateValues(values []string) []string {
	seen := make(map[string]bool, len(values))
	var duplicates []string
	for _, value := range values {
		if seen[value] {
			duplicates = append(duplicates, value)
		}
		seen[value] = true
	}
	return duplicates
}

func TestModelTraceDiagnosticModelsAddFingerprintlessCatalogModel(t *testing.T) {
	bankModels, err := ModelTraceGPTModels()
	require.NoError(t, err)
	require.NotContains(t, bankModels, "gpt-6.1-sol", "the bundled bank has no 6.1-sol class")

	diagnostic, err := ModelTraceDiagnosticModels()
	require.NoError(t, err)
	require.Contains(t, diagnostic, "gpt-6.1-sol")
	require.Len(t, diagnostic, len(bankModels)+1, "extra models must not duplicate bank classes")
	for _, model := range bankModels {
		require.Contains(t, diagnostic, model)
	}
	require.Empty(t, duplicateValues(diagnostic))
}

// TestCodexDegradationVerdicts 覆盖判定四态：normal / degraded / uncertain / failed。
func TestCodexDegradationVerdicts(t *testing.T) {
	text, expected := referenceSampleText(t, "gpt-6-sol")
	challenge := ModelTraceChallenge{Prompt: "reference", ExpectedCount: expected}

	t.Run("exact class match is normal", func(t *testing.T) {
		verdict := EvaluateCodexDiagnostic("gpt-6-sol", text, challenge)
		require.Equal(t, CodexDegradationStatusNormal, verdict.Status)
		require.Empty(t, verdict.Reason)
		require.Equal(t, "gpt-6-sol", verdict.PredictedModel)
		require.Greater(t, verdict.Probability, 0.0)
	})

	t.Run("fingerprint-less model only reaches uncertain", func(t *testing.T) {
		verdict := EvaluateCodexDiagnostic("gpt-6.1-sol", text, challenge)
		require.Equal(t, CodexDegradationStatusUncertain, verdict.Status)
		require.Equal(t, "reference_class_unavailable", verdict.Reason)
		require.Equal(t, "gpt-6-sol", verdict.PredictedModel)
	})

	t.Run("any other class is degraded", func(t *testing.T) {
		verdict := EvaluateCodexDiagnostic("gpt-5.6-luna", text, challenge)
		require.Equal(t, CodexDegradationStatusDegraded, verdict.Status)
		require.Equal(t, "fingerprint_mismatch", verdict.Reason)
		require.Equal(t, "gpt-6-sol", verdict.PredictedModel)
	})

	t.Run("truncated output fails instead of degrading", func(t *testing.T) {
		verdict := EvaluateCodexDiagnostic("gpt-6-sol", "1 2 3", challenge)
		require.Equal(t, CodexDegradationStatusFailed, verdict.Status)
		require.Equal(t, "insufficient_numbers", verdict.Reason)
		require.Equal(t, 3, verdict.ParsedCount)
		require.Empty(t, verdict.PredictedModel)
	})
}
