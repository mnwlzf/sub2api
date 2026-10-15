package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// --- fakes for the candy judge credential / routing checks ---

// roundTripPelicanEncryptor is a reversible stand-in for SecretEncryptor: the
// production fake (fakePelicanEncryptor) ignores its input, which cannot
// distinguish "judge key stored" from "judge key dropped". The ciphertext is
// base64 so it does not literally contain the plaintext, which lets the tests
// assert that no plaintext leaks into storage or API payloads.
type roundTripPelicanEncryptor struct{}

func (roundTripPelicanEncryptor) Encrypt(plaintext string) (string, error) {
	return "enc:" + base64.StdEncoding.EncodeToString([]byte(plaintext)), nil
}

func (roundTripPelicanEncryptor) Decrypt(ciphertext string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(ciphertext, "enc:"))
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// mustEncrypt encrypts with the test encryptor and fails the test on error.
func (roundTripPelicanEncryptor) mustEncrypt(t *testing.T, plaintext string) string {
	t.Helper()
	ciphertext, err := roundTripPelicanEncryptor{}.Encrypt(plaintext)
	require.NoError(t, err)
	return ciphertext
}

type judgeDiagCall struct {
	GroupID  int64
	Model    string
	Platform string
}

// fakeJudgeModels answers the availability diagnosis from fixed tables.
type fakeJudgeModels struct {
	supported map[int64]map[string]bool
	emptyPool map[int64]bool
	calls     []judgeDiagCall
}

func (f *fakeJudgeModels) DiagnoseModelAvailabilityForPlatform(_ context.Context, groupID *int64, requestedModel, platform string) ModelAvailabilityDiagnosis {
	id := int64(0)
	if groupID != nil {
		id = *groupID
	}
	f.calls = append(f.calls, judgeDiagCall{GroupID: id, Model: requestedModel, Platform: platform})
	if f.emptyPool[id] {
		return ModelAvailabilityDiagnosis{HasAccountsInPool: false, HasModelSupport: false}
	}
	return ModelAvailabilityDiagnosis{HasAccountsInPool: true, HasModelSupport: f.supported[id][requestedModel]}
}

type fakeJudgeGroups struct {
	platforms map[int64]string
}

func (f fakeJudgeGroups) GetByID(_ context.Context, id int64) (*Group, error) {
	platform, ok := f.platforms[id]
	if !ok {
		return nil, nil
	}
	return &Group{ID: id, Platform: platform}, nil
}

type fakeJudgeKeys struct {
	keys map[string]*APIKey
	err  error
}

func (f fakeJudgeKeys) GetByKey(_ context.Context, key string) (*APIKey, error) {
	if f.err != nil {
		return nil, f.err
	}
	found, ok := f.keys[key]
	if !ok {
		return nil, ErrAPIKeyNotFound
	}
	return found, nil
}

func pelicanInt64Ptr(v int64) *int64 { return &v }

// judgeEnvForProductionScenario reproduces the reported production setup:
// probe group #12 is an openai group, the judge model only lives in the
// deepseek groups #42/#52, and the judge key is bound to group #42.
func judgeEnvForProductionScenario() (PelicanJudgeEnv, *fakeJudgeModels) {
	models := &fakeJudgeModels{
		supported: map[int64]map[string]bool{
			12: {"gpt-5": true},
			42: {"deepseek-v4.1-flash": true},
		},
	}
	return PelicanJudgeEnv{
		Models: models,
		Groups: fakeJudgeGroups{platforms: map[int64]string{
			12: PlatformOpenAI,
			42: PlatformDeepseek,
		}},
		Keys: fakeJudgeKeys{keys: map[string]*APIKey{
			"judge-key-42": {ID: 7, GroupID: pelicanInt64Ptr(42), Group: &Group{ID: 42, Platform: PlatformDeepseek}},
		}},
	}, models
}

// --- plan persistence of the judge credentials ---

func TestCreatePlanCandyJudgeCredentials(t *testing.T) {
	newSvc := func() (*PelicanProbeService, *fakePelicanPlanRepo) {
		plans := newFakePelicanPlanRepo()
		env, _ := judgeEnvForProductionScenario()
		svc := &PelicanProbeService{
			planRepo:  plans,
			encryptor: roundTripPelicanEncryptor{},
			judge:     env,
		}
		return svc, plans
	}
	base := CreatePlanInput{GroupID: 12, ModelID: "gpt-5", APIKey: "probe-key", TestType: "candy"}

	t.Run("judge key is encrypted at rest and never echoed", func(t *testing.T) {
		svc, plans := newSvc()
		in := base
		in.JudgeModelID = "deepseek-v4.1-flash"
		in.JudgeAPIKey = "judge-key-42"
		got, err := svc.CreatePlan(context.Background(), in)
		require.NoError(t, err)

		stored := plans.byID[got.ID]
		require.Equal(t, roundTripPelicanEncryptor{}.mustEncrypt(t, "judge-key-42"), stored.JudgeAPIKeyEncrypted,
			"judge key must be stored encrypted")
		require.NotContains(t, stored.JudgeAPIKeyEncrypted, "judge-key-42")

		require.True(t, got.HasJudgeAPIKey)
		require.NotContains(t, pelicanPlanJSON(t, got), "judge-key-42", "plaintext judge key must never be serialized")
		require.Contains(t, pelicanPlanJSON(t, got), `"has_judge_api_key":true`)
	})

	t.Run("blank judge key leaves has_judge_api_key false", func(t *testing.T) {
		svc, plans := newSvc()
		in := base
		in.JudgeModelID = "gpt-5" // served by the probe group, so no judge key is needed
		in.JudgeAPIKey = "   "
		got, err := svc.CreatePlan(context.Background(), in)
		require.NoError(t, err)
		require.False(t, got.HasJudgeAPIKey)
		require.Empty(t, plans.byID[got.ID].JudgeAPIKeyEncrypted)
		require.Contains(t, pelicanPlanJSON(t, got), `"has_judge_api_key":false`)
	})

	t.Run("judge base url round trips", func(t *testing.T) {
		svc, plans := newSvc()
		in := base
		in.JudgeModelID = "gpt-5"
		in.JudgeBaseURL = "  https://judge.example.com  "
		got, err := svc.CreatePlan(context.Background(), in)
		require.NoError(t, err)
		require.Equal(t, "https://judge.example.com", got.JudgeBaseURL)
		require.Equal(t, "https://judge.example.com", plans.byID[got.ID].JudgeBaseURL)
	})

	t.Run("pelican plan ignores judge credentials", func(t *testing.T) {
		svc, plans := newSvc()
		in := base
		in.TestType = "pelican"
		in.JudgeModelID = "deepseek-v4.1-flash"
		in.JudgeAPIKey = "judge-key-42"
		got, err := svc.CreatePlan(context.Background(), in)
		require.NoError(t, err)
		require.False(t, got.HasJudgeAPIKey)
		require.Empty(t, plans.byID[got.ID].JudgeAPIKeyEncrypted)
	})
}

func TestUpdatePlanCandyJudgeCredentials(t *testing.T) {
	newSvc := func(existing *PelicanProbePlanRow) (*PelicanProbeService, *fakePelicanPlanRepo) {
		plans := newFakePelicanPlanRepo()
		plans.byID[existing.ID] = existing
		env, _ := judgeEnvForProductionScenario()
		svc := &PelicanProbeService{
			planRepo:  plans,
			encryptor: roundTripPelicanEncryptor{},
			judge:     env,
		}
		return svc, plans
	}
	candyBase := func() *PelicanProbePlanRow {
		return &PelicanProbePlanRow{
			PelicanProbePlan: PelicanProbePlan{
				ID: 7, GroupID: 12, ModelID: "gpt-5", TestType: PelicanTestTypeCandy,
				JudgeModelID:   "deepseek-v4.1-flash",
				CronExpression: "0 */4 * * *", Enabled: true, MaxResults: 50,
			},
			Prompt:          DefaultCandyPrompt,
			APIKeyEncrypted: "enc:probe-key",
		}
	}

	t.Run("judge key is replaced encrypted", func(t *testing.T) {
		svc, plans := newSvc(candyBase())
		got, err := svc.UpdatePlan(context.Background(), 7, UpdatePlanInput{JudgeAPIKey: "judge-key-42"})
		require.NoError(t, err)
		require.Equal(t, roundTripPelicanEncryptor{}.mustEncrypt(t, "judge-key-42"), plans.byID[7].JudgeAPIKeyEncrypted)
		require.True(t, got.HasJudgeAPIKey)
		require.NotContains(t, pelicanPlanJSON(t, got), "judge-key-42")
	})

	t.Run("blank judge key keeps the stored one", func(t *testing.T) {
		existing := candyBase()
		existing.JudgeAPIKeyEncrypted = roundTripPelicanEncryptor{}.mustEncrypt(t, "judge-key-42")
		svc, plans := newSvc(existing)
		got, err := svc.UpdatePlan(context.Background(), 7, UpdatePlanInput{Name: pelicanStrPtr("renamed")})
		require.NoError(t, err)
		require.Equal(t, existing.JudgeAPIKeyEncrypted, plans.byID[7].JudgeAPIKeyEncrypted)
		require.True(t, got.HasJudgeAPIKey)
	})

	t.Run("judge base url updated and cleared", func(t *testing.T) {
		svc, plans := newSvc(candyBase())
		got, err := svc.UpdatePlan(context.Background(), 7, UpdatePlanInput{JudgeBaseURL: pelicanStrPtr("https://judge.example.com")})
		require.NoError(t, err)
		require.Equal(t, "https://judge.example.com", got.JudgeBaseURL)

		got, err = svc.UpdatePlan(context.Background(), 7, UpdatePlanInput{JudgeBaseURL: pelicanStrPtr("")})
		require.NoError(t, err)
		require.Empty(t, got.JudgeBaseURL)
		require.Empty(t, plans.byID[7].JudgeBaseURL)
	})
}

// --- create/update routing validation ---

func TestCreatePlanCandyJudgeRouting(t *testing.T) {
	newSvc := func() (*PelicanProbeService, *fakeJudgeModels) {
		plans := newFakePelicanPlanRepo()
		env, models := judgeEnvForProductionScenario()
		svc := &PelicanProbeService{planRepo: plans, encryptor: roundTripPelicanEncryptor{}, judge: env}
		return svc, models
	}
	base := CreatePlanInput{GroupID: 12, ModelID: "gpt-5", APIKey: "probe-key", TestType: "candy"}

	t.Run("judge model missing is rejected", func(t *testing.T) {
		svc, _ := newSvc()
		_, err := svc.CreatePlan(context.Background(), base)
		require.ErrorContains(t, err, "judge_model_id is required")
	})

	t.Run("judge key group not serving the judge model is rejected", func(t *testing.T) {
		svc, models := newSvc()
		in := base
		in.JudgeModelID = "gpt-5" // only group #12 serves it, the key is bound to #42
		in.JudgeAPIKey = "judge-key-42"
		_, err := svc.CreatePlan(context.Background(), in)
		require.Error(t, err)
		require.Contains(t, err.Error(), `judge model "gpt-5" is not available in the judge key's group (#42)`)
		require.Contains(t, err.Error(), "provide a key bound to a group that serves it")
		require.Equal(t, []judgeDiagCall{{GroupID: 42, Model: "gpt-5", Platform: PlatformDeepseek}}, models.calls)
	})

	t.Run("judge key group serving the judge model passes", func(t *testing.T) {
		svc, models := newSvc()
		in := base
		in.JudgeModelID = "deepseek-v4.1-flash"
		in.JudgeAPIKey = "judge-key-42"
		_, err := svc.CreatePlan(context.Background(), in)
		require.NoError(t, err)
		require.Equal(t, []judgeDiagCall{{GroupID: 42, Model: "deepseek-v4.1-flash", Platform: PlatformDeepseek}}, models.calls)
	})

	t.Run("probe group not serving the judge model is rejected", func(t *testing.T) {
		svc, models := newSvc()
		in := base
		in.JudgeModelID = "deepseek-v4.1-flash" // lives in #42, probe group is #12
		_, err := svc.CreatePlan(context.Background(), in)
		require.Error(t, err)
		require.Contains(t, err.Error(), `judge model "deepseek-v4.1-flash" is not available in probe group (#12)`)
		require.Contains(t, err.Error(), "provide a judge key bound to a group that serves it")
		require.Equal(t, []judgeDiagCall{{GroupID: 12, Model: "deepseek-v4.1-flash", Platform: PlatformOpenAI}}, models.calls)
	})

	t.Run("probe group serving the judge model passes", func(t *testing.T) {
		svc, _ := newSvc()
		in := base
		in.JudgeModelID = "gpt-5"
		_, err := svc.CreatePlan(context.Background(), in)
		require.NoError(t, err)
	})

	t.Run("unknown judge key is rejected", func(t *testing.T) {
		svc, _ := newSvc()
		in := base
		in.JudgeModelID = "deepseek-v4.1-flash"
		in.JudgeAPIKey = "nope"
		_, err := svc.CreatePlan(context.Background(), in)
		require.ErrorIs(t, err, ErrPelicanJudgeKeyNotFound)
	})

	t.Run("judge key without a group stays inconclusive", func(t *testing.T) {
		svc, models := newSvc()
		svc.judge.Keys = fakeJudgeKeys{keys: map[string]*APIKey{
			"user-key": {ID: 9, GroupID: nil},
		}}
		in := base
		in.JudgeModelID = "deepseek-v4.1-flash"
		in.JudgeAPIKey = "user-key"
		_, err := svc.CreatePlan(context.Background(), in)
		require.NoError(t, err)
		require.Empty(t, models.calls, "no group to diagnose means no diagnosis call")
	})

	t.Run("group with no accounts stays inconclusive", func(t *testing.T) {
		svc, _ := newSvc()
		svc.judge.Models = &fakeJudgeModels{emptyPool: map[int64]bool{12: true}}
		in := base
		in.JudgeModelID = "deepseek-v4.1-flash"
		_, err := svc.CreatePlan(context.Background(), in)
		require.NoError(t, err)
	})

	t.Run("pelican plan never triggers the routing check", func(t *testing.T) {
		svc, models := newSvc()
		in := base
		in.TestType = "pelican"
		in.JudgeModelID = "deepseek-v4.1-flash"
		_, err := svc.CreatePlan(context.Background(), in)
		require.NoError(t, err)
		require.Empty(t, models.calls)
	})

	t.Run("missing diagnosis dependencies stay inconclusive", func(t *testing.T) {
		plans := newFakePelicanPlanRepo()
		svc := &PelicanProbeService{planRepo: plans, encryptor: roundTripPelicanEncryptor{}}
		in := base
		in.JudgeModelID = "deepseek-v4.1-flash"
		_, err := svc.CreatePlan(context.Background(), in)
		require.NoError(t, err)
	})
}

func TestUpdatePlanCandyJudgeRouting(t *testing.T) {
	newSvc := func() (*PelicanProbeService, *fakePelicanPlanRepo, *fakeJudgeModels) {
		plans := newFakePelicanPlanRepo()
		plans.byID[7] = &PelicanProbePlanRow{
			PelicanProbePlan: PelicanProbePlan{
				ID: 7, GroupID: 12, ModelID: "gpt-5", TestType: PelicanTestTypeCandy,
				CronExpression: "0 */4 * * *", Enabled: true, MaxResults: 50,
			},
			Prompt:          DefaultCandyPrompt,
			APIKeyEncrypted: "enc:probe-key",
		}
		env, models := judgeEnvForProductionScenario()
		svc := &PelicanProbeService{planRepo: plans, encryptor: roundTripPelicanEncryptor{}, judge: env}
		return svc, plans, models
	}

	t.Run("judge model unavailable in probe group is rejected", func(t *testing.T) {
		svc, plans, _ := newSvc()
		_, err := svc.UpdatePlan(context.Background(), 7, UpdatePlanInput{JudgeModelID: pelicanStrPtr("deepseek-v4.1-flash")})
		require.ErrorContains(t, err, `judge model "deepseek-v4.1-flash" is not available in probe group (#12)`)
		require.Empty(t, plans.byID[7].JudgeModelID, "rejected update must not be persisted")
	})

	t.Run("judge model available in probe group passes", func(t *testing.T) {
		svc, _, _ := newSvc()
		got, err := svc.UpdatePlan(context.Background(), 7, UpdatePlanInput{JudgeModelID: pelicanStrPtr("gpt-5")})
		require.NoError(t, err)
		require.Equal(t, "gpt-5", got.JudgeModelID)
	})

	t.Run("new judge key is used to resolve the group", func(t *testing.T) {
		svc, _, models := newSvc()
		got, err := svc.UpdatePlan(context.Background(), 7, UpdatePlanInput{
			JudgeModelID: pelicanStrPtr("deepseek-v4.1-flash"),
			JudgeAPIKey:  "judge-key-42",
		})
		require.NoError(t, err)
		require.Equal(t, "deepseek-v4.1-flash", got.JudgeModelID)
		require.True(t, got.HasJudgeAPIKey)
		require.Equal(t, []judgeDiagCall{{GroupID: 42, Model: "deepseek-v4.1-flash", Platform: PlatformDeepseek}}, models.calls)
	})

	t.Run("stored judge key is reused when the request omits it", func(t *testing.T) {
		svc, plans, models := newSvc()
		plans.byID[7].JudgeAPIKeyEncrypted = roundTripPelicanEncryptor{}.mustEncrypt(t, "judge-key-42")
		got, err := svc.UpdatePlan(context.Background(), 7, UpdatePlanInput{JudgeModelID: pelicanStrPtr("deepseek-v4.1-flash")})
		require.NoError(t, err)
		require.Equal(t, "deepseek-v4.1-flash", got.JudgeModelID)
		require.Equal(t, []judgeDiagCall{{GroupID: 42, Model: "deepseek-v4.1-flash", Platform: PlatformDeepseek}}, models.calls)
	})

	t.Run("toggling enabled does not re-validate legacy plans", func(t *testing.T) {
		svc, plans, models := newSvc()
		plans.byID[7].JudgeModelID = "deepseek-v4.1-flash" // legacy: unroutable, but stored
		got, err := svc.UpdatePlan(context.Background(), 7, UpdatePlanInput{Enabled: pelicanBoolPtr(false)})
		require.NoError(t, err)
		require.False(t, got.Enabled)
		require.Empty(t, models.calls)
	})
}

// --- judge call endpoint resolution ---

// judgeEndpointServers returns two fake gateways: one for the probe and one for
// the judge, each recording the Authorization header it received. The probe
// gateway always answers the candy answer "21"; the judge gateway answers
// judgeContent. When the plan has no judge_base_url the judge call lands on the
// probe gateway instead, which is why probeAuth is recorded there too.
func judgeEndpointServers(t *testing.T, judgeContent string) (probeURL, judgeURL string, probeAuth, judgeAuth *string) {
	t.Helper()
	probeAuth, judgeAuth = new(string), new(string)
	probe := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*probeAuth = r.Header.Get("Authorization")
		writeChatContent(w, "21")
	}))
	judge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*judgeAuth = r.Header.Get("Authorization")
		writeChatContent(w, judgeContent)
	}))
	t.Cleanup(func() { probe.Close(); judge.Close() })
	return probe.URL, judge.URL, probeAuth, judgeAuth
}

// judgeEndpointWithProbeContent returns a single fake gateway that serves both
// the probe and the judge call, keyed by the requested model name. It records
// the Authorization header per model, which is what the credential fallback
// assertions need.
func judgeEndpointWithProbeContent(t *testing.T, content string) (url string, authByModel map[string]string) {
	t.Helper()
	authByModel = map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var decoded map[string]any
		_ = json.Unmarshal(body, &decoded)
		model, _ := decoded["model"].(string)
		authByModel[model] = r.Header.Get("Authorization")
		writeChatContent(w, content)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, authByModel
}

func runCandyJudgeWithCredentials(t *testing.T, row *PelicanProbePlanRow, client *http.Client) (*PelicanProbeResult, error) {
	t.Helper()
	svc := &PelicanProbeService{
		planRepo: newFakePelicanPlanRepo(), resultRepo: &fakePelicanResultRepo{honorCtxCancel: true},
		encryptor: roundTripPelicanEncryptor{}, httpClient: client,
	}
	return svc.runRow(context.Background(), row, false)
}

func TestRunRowCandyJudgeCredentialFallback(t *testing.T) {
	t.Run("judge key and gateway override the probe ones", func(t *testing.T) {
		probeURL, judgeURL, probeAuth, judgeAuth := judgeEndpointServers(t, "YES")
		row := candyJudgeRow(probeURL)
		row.APIKeyEncrypted = roundTripPelicanEncryptor{}.mustEncrypt(t, "probe-key")
		row.JudgeAPIKeyEncrypted = roundTripPelicanEncryptor{}.mustEncrypt(t, "judge-key")
		row.JudgeBaseURL = judgeURL

		saved, err := runCandyJudgeWithCredentials(t, row, http.DefaultClient)
		require.NoError(t, err)
		require.Equal(t, PelicanVerdictPass, saved.Verdict)
		require.Equal(t, "Bearer probe-key", *probeAuth)
		require.Equal(t, "Bearer judge-key", *judgeAuth, "judge must use the judge key")
	})

	t.Run("blank judge credentials fall back to the probe ones", func(t *testing.T) {
		gatewayURL, authByModel := judgeEndpointWithProbeContent(t, "YES")
		row := candyJudgeRow(gatewayURL)
		row.APIKeyEncrypted = roundTripPelicanEncryptor{}.mustEncrypt(t, "probe-key")

		saved, err := runCandyJudgeWithCredentials(t, row, http.DefaultClient)
		require.NoError(t, err)
		require.Equal(t, PelicanVerdictPass, saved.Verdict)
		require.Equal(t, "Bearer probe-key", authByModel["probe-model"])
		require.Equal(t, "Bearer probe-key", authByModel["judge-model"], "unset judge key reuses the probe key")
	})

	t.Run("judge base url alone redirects the judge call", func(t *testing.T) {
		probeURL, judgeURL, probeAuth, judgeAuth := judgeEndpointServers(t, "NO")
		row := candyJudgeRow(probeURL)
		row.APIKeyEncrypted = roundTripPelicanEncryptor{}.mustEncrypt(t, "probe-key")
		row.JudgeBaseURL = judgeURL

		saved, err := runCandyJudgeWithCredentials(t, row, http.DefaultClient)
		require.NoError(t, err)
		require.Equal(t, PelicanVerdictDegraded, saved.Verdict)
		require.Equal(t, "Bearer probe-key", *probeAuth)
		require.Equal(t, "Bearer probe-key", *judgeAuth, "unset judge key reuses the probe key")
	})

	t.Run("undecryptable judge key leaves the verdict pending", func(t *testing.T) {
		probeURL, _, _, _ := judgeEndpointServers(t, "YES")
		row := candyJudgeRow(probeURL)
		row.APIKeyEncrypted = "enc:probe-key"
		row.JudgeAPIKeyEncrypted = "not-decryptable"

		svc := &PelicanProbeService{
			planRepo: newFakePelicanPlanRepo(), resultRepo: &fakePelicanResultRepo{honorCtxCancel: true},
			encryptor: failingJudgeDecryptor{}, httpClient: http.DefaultClient,
		}
		saved, err := svc.runRow(context.Background(), row, false)
		require.NoError(t, err)
		require.Equal(t, PelicanVerdictPending, saved.Verdict)
		require.Contains(t, saved.Note, "decrypt judge api key")
	})
}

// failingJudgeDecryptor fails decryption only for the judge key ciphertext, so
// the probe endpoint still resolves.
type failingJudgeDecryptor struct{}

func (failingJudgeDecryptor) Encrypt(plaintext string) (string, error) {
	return "enc:" + plaintext, nil
}

func (failingJudgeDecryptor) Decrypt(ciphertext string) (string, error) {
	if strings.HasPrefix(ciphertext, "enc:") {
		return strings.TrimPrefix(ciphertext, "enc:"), nil
	}
	return "", io.ErrUnexpectedEOF
}

func TestSanitizePlanJudgeAPIKey(t *testing.T) {
	p := sanitizePlan(&PelicanProbePlanRow{JudgeAPIKeyEncrypted: "cipher"})
	require.True(t, p.HasJudgeAPIKey)

	p = sanitizePlan(&PelicanProbePlanRow{})
	require.False(t, p.HasJudgeAPIKey)

	// The row-level ciphertext must never reach the API payload.
	row := &PelicanProbePlanRow{
		PelicanProbePlan:     PelicanProbePlan{ID: 1, JudgeModelID: "judge-model", JudgeBaseURL: "https://judge.example.com"},
		APIKeyEncrypted:      "probe-cipher",
		JudgeAPIKeyEncrypted: "judge-cipher",
	}
	raw, err := json.Marshal(sanitizePlan(row))
	require.NoError(t, err)
	require.NotContains(t, string(raw), "judge-cipher")
	require.NotContains(t, string(raw), "probe-cipher")
	require.Contains(t, string(raw), `"judge_base_url":"https://judge.example.com"`)
}

// pelicanPlanJSON serializes a sanitized plan exactly as the HTTP layer would.
func pelicanPlanJSON(t *testing.T, p *PelicanProbePlan) string {
	t.Helper()
	raw, err := json.Marshal(p)
	require.NoError(t, err)
	return string(raw)
}
