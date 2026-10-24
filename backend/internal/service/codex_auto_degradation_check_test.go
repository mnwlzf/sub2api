package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

var errCodexDegradationProbeBoom = errors.New("boom")

func TestCodexDegradationSettingsDefaults(t *testing.T) {
	settingsService := NewSettingService(&upstreamBillingProbeSettingRepo{}, nil)
	settings, err := settingsService.GetCodexDegradationSettings(context.Background())
	require.NoError(t, err)
	require.False(t, settings.Enabled, "feature must be OFF by default (no surprise upstream traffic)")
	require.Equal(t, 10, settings.IntervalMinutes)
	require.Equal(t, 30, settings.BlockMinutes)
	require.Equal(t, []string{"gpt-6.1-sol"}, settings.Models)
	require.Equal(t, DefaultCodexDegradationSettings(), *settings)
}

func TestCodexDegradationSettingsRoundTrip(t *testing.T) {
	settingRepo := &upstreamBillingProbeSettingRepo{}
	settingsService := NewSettingService(settingRepo, nil)
	ctx := context.Background()

	want := &CodexDegradationSettings{
		Enabled:         true,
		IntervalMinutes: 7,
		BlockMinutes:    45,
		Models:          []string{"gpt-6.1-sol"},
	}
	require.NoError(t, settingsService.SetCodexDegradationSettings(ctx, want))
	got, err := settingsService.GetCodexDegradationSettings(ctx)
	require.NoError(t, err)
	require.Equal(t, want, got)

	raw := settingRepo.values[SettingKeyCodexDegradation]
	var stored CodexDegradationSettings
	require.NoError(t, json.Unmarshal([]byte(raw), &stored))
	require.Equal(t, *want, stored)
}

func TestCodexDegradationSettingsValidation(t *testing.T) {
	settingRepo := &upstreamBillingProbeSettingRepo{}
	settingsService := NewSettingService(settingRepo, nil)
	ctx := context.Background()

	valid := &CodexDegradationSettings{Enabled: true, IntervalMinutes: 10, BlockMinutes: 30, Models: []string{"gpt-6.1-sol"}}
	require.NoError(t, ValidateCodexDegradationSettings(valid))
	require.NoError(t, settingsService.SetCodexDegradationSettings(ctx, valid))

	badInterval := *valid
	badInterval.IntervalMinutes = 0
	require.ErrorContains(t, settingsService.SetCodexDegradationSettings(ctx, &badInterval), "interval_minutes must be at least 1")

	badBlock := *valid
	badBlock.BlockMinutes = 0
	require.ErrorContains(t, settingsService.SetCodexDegradationSettings(ctx, &badBlock), "block_minutes must be at least 1")

	unknownModel := *valid
	unknownModel.Models = []string{"gpt-9-does-not-exist"}
	require.ErrorContains(t, settingsService.SetCodexDegradationSettings(ctx, &unknownModel), "unknown diagnostic model")

	duplicate := *valid
	duplicate.Models = []string{"gpt-6.1-sol", "gpt-6.1-sol"}
	require.ErrorContains(t, settingsService.SetCodexDegradationSettings(ctx, &duplicate), "duplicate diagnostic model")

	empty := *valid
	empty.Models = []string{}
	require.ErrorContains(t, settingsService.SetCodexDegradationSettings(ctx, &empty), "models must not be empty")

	// 被拒绝的配置绝不落库：仍是上面保存的合法值。
	got, err := settingsService.GetCodexDegradationSettings(ctx)
	require.NoError(t, err)
	require.Equal(t, valid, got)
}

func TestCodexDegradationSettingsCorruptionFallsBackToDefaults(t *testing.T) {
	settingRepo := &upstreamBillingProbeSettingRepo{values: map[string]string{
		SettingKeyCodexDegradation: `{"enabled":`,
	}}
	settingsService := NewSettingService(settingRepo, nil)
	settings, err := settingsService.GetCodexDegradationSettings(context.Background())
	require.NoError(t, err)
	require.Equal(t, DefaultCodexDegradationSettings(), *settings)
}

func TestCodexDegradationCheckHealthyProbe(t *testing.T) {
	base := time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)
	account := activeOpenAIOAuthAccount(1)
	repo := newCodexDegradationRepoStub(account)
	probe := fixedCodexDegradationVerdict(CodexDegradationStatusNormal)
	svc := newCodexDegradationTestService(repo, &upstreamBillingProbeSettingRepo{values: enabledCodexDegradationSettings()}, CodexDegradationDeps{})
	svc.probe = probe
	svc.now = func() time.Time { return base }

	settings, err := svc.getSettings(context.Background())
	require.NoError(t, err)
	require.NoError(t, svc.runAccountCheck(context.Background(), account.ID, settings))

	require.Empty(t, repo.setCalls, "healthy probe must not block")
	require.Empty(t, repo.clearCalls, "nothing to clear")
	require.Equal(t, 1, probe.callCount())

	state := repo.storedState(t, account.ID)
	require.Equal(t, CodexDegradationStatusNormal, state.Status)
	require.Equal(t, base, state.CheckedAt)
	require.Equal(t, base.Add(10*time.Minute), state.NextCheckAt)
	require.Nil(t, state.BlockedUntil)
	require.Equal(t, []string{"gpt-6.1-sol"}, state.Models)
}

func TestCodexDegradationCheckDegradedBlocksAndRechecksAfterBlock(t *testing.T) {
	base := time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)
	account := activeOpenAIOAuthAccount(1)
	repo := newCodexDegradationRepoStub(account)
	probe := fixedCodexDegradationVerdict(CodexDegradationStatusDegraded)
	svc := newCodexDegradationTestService(repo, &upstreamBillingProbeSettingRepo{values: enabledCodexDegradationSettings()}, CodexDegradationDeps{})
	svc.probe = probe
	now := base
	svc.now = func() time.Time { return now }

	settings, err := svc.getSettings(context.Background())
	require.NoError(t, err)
	require.NoError(t, svc.runAccountCheck(context.Background(), account.ID, settings))

	require.Len(t, repo.setCalls, 1)
	first := repo.setCalls[0]
	require.Equal(t, base.Add(30*time.Minute), first.until)
	require.True(t, IsCodexDegradationBlockReason(first.reason), "block reason must carry the feature marker")
	require.Equal(t, account.ID, first.accountID)
	require.Empty(t, repo.clearCalls)

	state := repo.storedState(t, account.ID)
	require.Equal(t, CodexDegradationStatusDegraded, state.Status)
	require.Equal(t, base.Add(30*time.Minute), state.NextCheckAt, "re-test exactly after the block expires")
	require.NotNil(t, state.BlockedUntil)
	require.Equal(t, base.Add(30*time.Minute), *state.BlockedUntil)

	// 封锁到点后复查：仍然降级 → 封锁续期 30 分钟，next_check_at 再推 30 分钟。
	now = base.Add(30 * time.Minute)
	require.NoError(t, svc.runAccountCheck(context.Background(), account.ID, settings))

	require.Len(t, repo.setCalls, 2)
	second := repo.setCalls[1]
	require.Equal(t, base.Add(60*time.Minute), second.until)
	require.True(t, IsCodexDegradationBlockReason(second.reason))
	repo.mu.Lock()
	require.NotNil(t, account.TempUnschedulableUntil)
	require.Equal(t, base.Add(60*time.Minute), *account.TempUnschedulableUntil, "block must be extended while still degraded")
	repo.mu.Unlock()

	state = repo.storedState(t, account.ID)
	require.Equal(t, base.Add(60*time.Minute), state.NextCheckAt)
	require.Equal(t, 2, probe.callCount())
}

// TestCodexDegradationCheckHealthyClearsOnlyOwnBlocks 覆盖验收要求：
// 自动检测只解除自己写入的封锁，其它来源的封锁原样保留。
func TestCodexDegradationCheckHealthyClearsOnlyOwnBlocks(t *testing.T) {
	base := time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)

	t.Run("marker reason is cleared", func(t *testing.T) {
		account := activeOpenAIOAuthAccount(1)
		blockedUntil := base.Add(5 * time.Minute)
		account.TempUnschedulableUntil = &blockedUntil
		account.TempUnschedulableReason = `{"source":"codex_degradation_check","status":"degraded","models":["gpt-6.1-sol"],"until_unix":1790000000,"error_message":"codex degradation check: degraded"}`
		repo := newCodexDegradationRepoStub(account)
		svc := newCodexDegradationTestService(repo, &upstreamBillingProbeSettingRepo{values: enabledCodexDegradationSettings()}, CodexDegradationDeps{})
		svc.probe = fixedCodexDegradationVerdict(CodexDegradationStatusNormal)
		svc.now = func() time.Time { return base }

		settings, err := svc.getSettings(context.Background())
		require.NoError(t, err)
		require.NoError(t, svc.runAccountCheck(context.Background(), account.ID, settings))

		require.Equal(t, []int64{account.ID}, repo.clearCalls, "own block must be cleared on healthy probe")
		repo.mu.Lock()
		require.Nil(t, account.TempUnschedulableUntil)
		require.Empty(t, account.TempUnschedulableReason)
		repo.mu.Unlock()
		require.Empty(t, repo.setCalls)

		state := repo.storedState(t, account.ID)
		require.Equal(t, CodexDegradationStatusNormal, state.Status)
		require.Nil(t, state.BlockedUntil)
		require.Equal(t, base.Add(10*time.Minute), state.NextCheckAt)
	})

	t.Run("foreign reason survives healthy probe", func(t *testing.T) {
		for _, reason := range []string{
			`{"status_code":503,"until_unix":1790000000}`,
			"OAuth 401: unauthorized",
			"account_scheduling_threshold",
		} {
			account := activeOpenAIOAuthAccount(1)
			blockedUntil := base.Add(20 * time.Minute)
			account.TempUnschedulableUntil = &blockedUntil
			account.TempUnschedulableReason = reason
			repo := newCodexDegradationRepoStub(account)
			svc := newCodexDegradationTestService(repo, &upstreamBillingProbeSettingRepo{values: enabledCodexDegradationSettings()}, CodexDegradationDeps{})
			svc.probe = fixedCodexDegradationVerdict(CodexDegradationStatusUncertain)
			svc.now = func() time.Time { return base }

			settings, err := svc.getSettings(context.Background())
			require.NoError(t, err)
			require.NoError(t, svc.runAccountCheck(context.Background(), account.ID, settings))

			require.Empty(t, repo.clearCalls, "a block written by another subsystem must survive: %s", reason)
			repo.mu.Lock()
			require.NotNil(t, account.TempUnschedulableUntil)
			require.Equal(t, reason, account.TempUnschedulableReason)
			repo.mu.Unlock()

			state := repo.storedState(t, account.ID)
			require.Equal(t, CodexDegradationStatusUncertain, state.Status, "uncertain counts as healthy")
			require.Equal(t, base.Add(10*time.Minute), state.NextCheckAt)
		}
	})
}

func TestCodexDegradationCheckFailedProbeLeavesStateUntouched(t *testing.T) {
	base := time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)
	foreignReason := `{"status_code":503,"until_unix":1790000000}`

	cases := map[string]codexDegradationProbe{
		"failed verdict": fixedCodexDegradationVerdict(CodexDegradationStatusFailed),
		"probe error": &codexDegradationProbeStub{next: func(_ int64, _ string) (CodexDiagnosticVerdict, error) {
			return CodexDiagnosticVerdict{}, errCodexDegradationProbeBoom
		}},
	}
	for name, probe := range cases {
		t.Run(name, func(t *testing.T) {
			account := activeOpenAIOAuthAccount(1)
			// 预置一条其它子系统写的封锁：失败探测既不能解它，也不能覆盖它。
			blockedUntil := base.Add(20 * time.Minute)
			account.TempUnschedulableUntil = &blockedUntil
			account.TempUnschedulableReason = foreignReason
			repo := newCodexDegradationRepoStub(account)
			svc := newCodexDegradationTestService(repo, &upstreamBillingProbeSettingRepo{values: enabledCodexDegradationSettings()}, CodexDegradationDeps{})
			svc.probe = probe
			svc.now = func() time.Time { return base }

			settings, err := svc.getSettings(context.Background())
			require.NoError(t, err)
			require.NoError(t, svc.runAccountCheck(context.Background(), account.ID, settings))

			require.Empty(t, repo.setCalls, "failed probe must never create a block")
			require.Empty(t, repo.clearCalls, "failed probe must never clear a block")
			repo.mu.Lock()
			require.NotNil(t, account.TempUnschedulableUntil)
			require.Equal(t, foreignReason, account.TempUnschedulableReason)
			repo.mu.Unlock()

			state := repo.storedState(t, account.ID)
			require.Equal(t, CodexDegradationStatusFailed, state.Status)
			require.Equal(t, base.Add(10*time.Minute), state.NextCheckAt, "retry after the normal interval")
		})
	}
}

func TestCodexDegradationCheckSkipsAccountsBeforeNextCheck(t *testing.T) {
	base := time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)
	account := activeOpenAIOAuthAccount(1)
	account.Extra = map[string]any{
		CodexDegradationCheckExtraKey: &CodexDegradationCheckState{
			Status:      CodexDegradationStatusNormal,
			CheckedAt:   base.Add(-5 * time.Minute),
			NextCheckAt: base.Add(5 * time.Minute),
		},
	}
	repo := newCodexDegradationRepoStub(account)
	probe := fixedCodexDegradationVerdict(CodexDegradationStatusDegraded)
	svc := newCodexDegradationTestService(repo, &upstreamBillingProbeSettingRepo{values: enabledCodexDegradationSettings()}, CodexDegradationDeps{})
	svc.probe = probe
	svc.now = func() time.Time { return base }

	settings, err := svc.getSettings(context.Background())
	require.NoError(t, err)
	require.NoError(t, svc.runAccountCheck(context.Background(), account.ID, settings))

	require.Equal(t, 0, probe.callCount(), "not yet due → no probe")
	require.Empty(t, repo.setCalls)
	require.Empty(t, repo.clearCalls)
	require.Empty(t, repo.extraUpdates)
}

func TestCodexDegradationCheckRunDueGatesOnEnabledAndEligibility(t *testing.T) {
	base := time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)
	eligible := activeOpenAIOAuthAccount(1)
	ineligible := &Account{ID: 2, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Status: StatusActive}
	setupToken := activeOpenAIOAuthAccount(3)
	setupToken.Type = AccountTypeSetupToken

	t.Run("disabled runs nothing", func(t *testing.T) {
		repo := newCodexDegradationRepoStub(eligible, ineligible, setupToken)
		repo.dueAccounts = []Account{*eligible, *ineligible, *setupToken}
		probe := fixedCodexDegradationVerdict(CodexDegradationStatusDegraded)
		svc := newCodexDegradationTestService(repo, &upstreamBillingProbeSettingRepo{values: map[string]string{
			SettingKeyCodexDegradation: `{"enabled":false,"interval_minutes":10,"block_minutes":30,"models":["gpt-6.1-sol"]}`,
		}}, CodexDegradationDeps{})
		svc.probe = probe
		svc.now = func() time.Time { return base }

		require.NoError(t, svc.RunDue(context.Background()))
		require.Equal(t, 0, probe.callCount())
		require.Equal(t, 0, repo.listCallCount)
	})

	t.Run("enabled probes only eligible accounts", func(t *testing.T) {
		repo := newCodexDegradationRepoStub(eligible, ineligible, setupToken)
		repo.dueAccounts = []Account{*eligible, *ineligible, *setupToken}
		probe := fixedCodexDegradationVerdict(CodexDegradationStatusNormal)
		svc := newCodexDegradationTestService(repo, &upstreamBillingProbeSettingRepo{values: enabledCodexDegradationSettings()}, CodexDegradationDeps{})
		svc.probe = probe
		svc.now = func() time.Time { return base }

		require.NoError(t, svc.RunDue(context.Background()))
		require.Equal(t, 2, probe.callCount(), "OpenAI oauth + setup-token only")
		require.NotContains(t, probe.probedIDs(), int64(2), "non-OpenAI account must not be probed")
		require.Equal(t, 1, repo.listCallCount)
		// 首次运行立即到检：两个 eligible 账号都写入了 next_check_at。
		require.Equal(t, base.Add(10*time.Minute), repo.storedState(t, 1).NextCheckAt)
		require.Equal(t, base.Add(10*time.Minute), repo.storedState(t, 3).NextCheckAt)
		require.False(t, repo.hasStoredState(2), "ineligible account must stay untouched")
	})
}

func TestCodexDegradationCheckProbeModelsCombination(t *testing.T) {
	svc := &CodexDegradationService{}
	verdicts := map[string]string{}
	probe := &codexDegradationProbeStub{
		next: func(_ int64, model string) (CodexDiagnosticVerdict, error) {
			return CodexDiagnosticVerdict{Status: verdicts[model]}, nil
		},
	}
	svc.probe = probe

	// 任一 degraded → degraded（阳性证据优先）。
	verdicts = map[string]string{"a": CodexDegradationStatusDegraded, "b": CodexDegradationStatusFailed}
	status, reason, _ := svc.probeModels(context.Background(), 1, []string{"a", "b"}, "auto")
	require.Equal(t, CodexDegradationStatusDegraded, status)
	require.Contains(t, reason, "a")

	// 无 degraded 但有 failed → failed（不封锁、不解封）。
	verdicts = map[string]string{"a": CodexDegradationStatusNormal, "b": CodexDegradationStatusFailed}
	status, _, _ = svc.probeModels(context.Background(), 1, []string{"a", "b"}, "auto")
	require.Equal(t, CodexDegradationStatusFailed, status)

	// 全部 normal/uncertain → 健康。
	verdicts = map[string]string{"a": CodexDegradationStatusNormal, "b": CodexDegradationStatusUncertain}
	status, _, _ = svc.probeModels(context.Background(), 1, []string{"a", "b"}, "auto")
	require.Equal(t, CodexDegradationStatusUncertain, status)

	verdicts = map[string]string{"a": CodexDegradationStatusNormal, "b": CodexDegradationStatusNormal}
	status, _, _ = svc.probeModels(context.Background(), 1, []string{"a", "b"}, "auto")
	require.Equal(t, CodexDegradationStatusNormal, status)

	// 探针不可用 → failed，不产生任何封锁倾向。
	svc.probe = nil
	status, reason, results := svc.probeModels(context.Background(), 1, []string{"a"}, "auto")
	require.Equal(t, CodexDegradationStatusFailed, status)
	require.Equal(t, "a: probe_unavailable", reason)
	require.Equal(t, []CodexDegradationProbeResult{{Model: "a", Status: "failed", Reason: "probe_unavailable"}}, results)
}

func TestCodexDegradationCheckPersistsProbeEvidence(t *testing.T) {
	base := time.Date(2026, 10, 15, 8, 0, 0, 0, time.UTC)
	account := activeOpenAIOAuthAccount(1)
	repo := newCodexDegradationRepoStub(account)
	models := []string{"gpt-6.1-sol", "gpt-6-astra", "partial", "request-error"}
	probe := &codexDegradationProbeStub{next: func(_ int64, model string) (CodexDiagnosticVerdict, error) {
		switch model {
		case "gpt-6.1-sol":
			return CodexDiagnosticVerdict{Status: CodexDegradationStatusDegraded, PredictedModel: "gpt-5.6", Probability: 0.875, ParsedCount: 64, Reason: "fingerprint_mismatch"}, nil
		case "gpt-6-astra":
			return CodexDiagnosticVerdict{Status: CodexDegradationStatusNormal, PredictedModel: "gpt-6-astra", Probability: 0.95, ParsedCount: 64}, nil
		case "partial":
			return CodexDiagnosticVerdict{Status: CodexDegradationStatusFailed, ParsedCount: 3, Reason: "insufficient_numbers"}, nil
		default:
			return CodexDiagnosticVerdict{}, errCodexDegradationProbeBoom
		}
	}}
	svc := newCodexDegradationTestService(repo, &upstreamBillingProbeSettingRepo{values: enabledCodexDegradationSettings()}, CodexDegradationDeps{})
	svc.probe = probe
	svc.now = func() time.Time { return base }
	settings := &CodexDegradationSettings{Enabled: true, IntervalMinutes: 10, BlockMinutes: 30, Models: models}
	require.NoError(t, svc.runAccountCheck(context.Background(), account.ID, settings))

	// 断言持久化 JSON 契约，包括不伪造未获得的预测。
	rawState, err := json.Marshal(account.Extra)
	require.NoError(t, err)
	var stored map[string]any
	require.NoError(t, json.Unmarshal(rawState, &stored))
	state := decodeCodexDegradationCheckState(stored)
	require.NotNil(t, state)
	require.Equal(t, CodexDegradationStatusDegraded, state.Status)
	expectedJSON := `[
		{"model":"gpt-6.1-sol","status":"degraded","predicted_model":"gpt-5.6","probability":0.875,"parsed_count":64,"reason":"fingerprint_mismatch"},
		{"model":"gpt-6-astra","status":"normal","predicted_model":"gpt-6-astra","probability":0.95,"parsed_count":64},
		{"model":"partial","status":"failed","parsed_count":3,"reason":"insufficient_numbers"},
		{"model":"request-error","status":"failed","reason":"boom"}
	]`
	var persistedState map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(rawState, &persistedState))
	var stateFields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(persistedState[CodexDegradationCheckExtraKey], &stateFields))
	require.JSONEq(t, expectedJSON, string(stateFields["probe_results"]))

	var reasonFields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(account.TempUnschedulableReason), &reasonFields))
	require.JSONEq(t, expectedJSON, string(reasonFields["probe_results"]))
	require.True(t, IsCodexDegradationBlockReason(account.TempUnschedulableReason))
	display := tempUnschedStateFromStoredReason(account.TempUnschedulableReason, 0)
	require.Contains(t, display.ErrorMessage, "gpt-6.1-sol → gpt-5.6 (87.50%)")
	require.NotContains(t, display.ErrorMessage, "gpt-6-astra →", "healthy models must not be presented as degraded")
	require.Equal(t, base.Add(30*time.Minute).Unix(), display.UntilUnix)
}

func TestCodexDegradationCheckPreservesLongerForeignBlock(t *testing.T) {
	base := time.Date(2026, 10, 15, 8, 0, 0, 0, time.UTC)
	account := activeOpenAIOAuthAccount(1)
	until := base.Add(time.Hour)
	foreignReason := `{"source":"temp_unschedulable_rule","status_code":503,"error_message":"overloaded"}`
	account.TempUnschedulableUntil = &until
	account.TempUnschedulableReason = foreignReason
	repo := newCodexDegradationRepoStub(account)
	svc := newCodexDegradationTestService(repo, &upstreamBillingProbeSettingRepo{values: enabledCodexDegradationSettings()}, CodexDegradationDeps{})
	svc.probe = fixedCodexDegradationVerdict(CodexDegradationStatusDegraded)
	svc.now = func() time.Time { return base }
	settings, err := svc.getSettings(context.Background())
	require.NoError(t, err)
	require.NoError(t, svc.runAccountCheck(context.Background(), account.ID, settings))
	require.Equal(t, &until, account.TempUnschedulableUntil)
	require.Equal(t, foreignReason, account.TempUnschedulableReason)
	require.Equal(t, CodexDegradationStatusDegraded, repo.storedState(t, account.ID).Status)
	require.Empty(t, repo.clearCalls)
}

func TestCodexDegradationCheckStopIsIdempotent(t *testing.T) {
	repo := newCodexDegradationRepoStub()
	svc := newCodexDegradationTestService(repo, &upstreamBillingProbeSettingRepo{}, CodexDegradationDeps{})
	require.NotPanics(t, func() {
		svc.Start(context.Background())
		svc.Stop()
		svc.Stop()
	})
}

// TestCodexDegradationProbeTemplateSettingsRoundTrip 覆盖可编辑 JSONL 模板：
// 默认值、覆盖保存、非法模板拒绝保存。
func TestCodexDegradationProbeTemplateSettingsRoundTrip(t *testing.T) {
	settingRepo := &upstreamBillingProbeSettingRepo{}
	settingsService := NewSettingService(settingRepo, nil)
	ctx := context.Background()

	raw, err := settingsService.GetCodexProbeTemplateRaw(ctx)
	require.NoError(t, err)
	require.Equal(t, DefaultCodexProbeTemplate(), raw)

	custom := strings.Replace(DefaultCodexProbeTemplate(), "anonymous workspace", "custom workspace", 1)
	require.NoError(t, settingsService.SetCodexProbeTemplate(ctx, custom))
	raw, err = settingsService.GetCodexProbeTemplateRaw(ctx)
	require.NoError(t, err)
	require.Equal(t, custom, raw)
	template, err := settingsService.GetCodexProbeTemplate(ctx)
	require.NoError(t, err)
	require.Contains(t, template.messages[0].Content[0].Text, "custom workspace")

	require.ErrorIs(t, settingsService.SetCodexProbeTemplate(ctx, "{invalid"), ErrCodexProbeTemplateInvalid)
	raw, err = settingsService.GetCodexProbeTemplateRaw(ctx)
	require.NoError(t, err)
	require.Equal(t, custom, raw, "invalid template must not overwrite the stored one")
}
