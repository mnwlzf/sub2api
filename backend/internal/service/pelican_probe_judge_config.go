package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// This file adds the candy judge's *credential* plumbing on top of the judge
// prompt/verdict helpers in pelican_probe_service.go.
//
// Why the judge needs its own credentials: the judge model is frequently served
// by a different group (and platform) than the probe model. Reusing the probe
// key therefore routes the judge call into the probe group, where the judge
// model is often not configured at all — production showed group 12 (openai)
// probing with judge model deepseek-v4.1-flash, whose accounts live in group
// 42/52 (deepseek). The judge request 404'd with "Model not supported by any
// configured account in this group", so the verdict stayed pending forever.
//
// The fix is twofold: an optional judge key / judge gateway on the plan, and a
// create/update-time check that the judge model is actually served by the group
// the judge call will be routed to.

// PelicanJudgeKeyLookup resolves a plaintext API key to the group it is bound
// to. *APIKeyRepository satisfies it via GetByKey (which also loads the group).
type PelicanJudgeKeyLookup interface {
	GetByKey(ctx context.Context, key string) (*APIKey, error)
}

// PelicanJudgeGroupLookup resolves a group's platform so the judge model can be
// diagnosed against the right account pool. *GroupRepository satisfies it.
type PelicanJudgeGroupLookup interface {
	GetByID(ctx context.Context, id int64) (*Group, error)
}

// PelicanJudgeEnv bundles the lookups the candy judge validation needs. Every
// field is optional: a nil field means "cannot diagnose", and the validation
// then stays out of the way instead of blocking plan edits (mirrors the
// conservative fallback of DiagnoseModelAvailabilityForPlatform).
type PelicanJudgeEnv struct {
	// Models reports whether a group's account pool can serve a model.
	Models ModelAvailabilityDiagnoser
	// Groups resolves a group's platform.
	Groups PelicanJudgeGroupLookup
	// Keys resolves a plaintext judge key to its group.
	Keys PelicanJudgeKeyLookup
}

// ErrPelicanJudgeKeyNotFound reports a judge key that is not a valid API key.
var ErrPelicanJudgeKeyNotFound = errors.New("judge api key not found")

// resolveJudgeKeyGroup resolves the group a plaintext judge key routes to.
//
// A nil groupID (key not bound to any group) is not an error: routing then falls
// back to the owner's allowed groups, which this service cannot diagnose, so the
// caller treats the model check as inconclusive. The platform is best-effort and
// may be empty.
func (s *PelicanProbeService) resolveJudgeKeyGroup(ctx context.Context, plaintextKey string) (groupID *int64, platform string, err error) {
	plaintextKey = strings.TrimSpace(plaintextKey)
	if plaintextKey == "" || s.judge.Keys == nil {
		return nil, "", nil
	}
	key, err := s.judge.Keys.GetByKey(ctx, plaintextKey)
	if err != nil {
		if errors.Is(err, ErrAPIKeyNotFound) {
			return nil, "", fmt.Errorf("%w: the judge key is not a valid API key", ErrPelicanJudgeKeyNotFound)
		}
		return nil, "", fmt.Errorf("look up judge api key: %w", err)
	}
	if key == nil || key.GroupID == nil {
		return nil, "", nil
	}
	if key.Group != nil {
		platform = strings.TrimSpace(key.Group.Platform)
	}
	if platform == "" && s.judge.Groups != nil {
		if g, gerr := s.judge.Groups.GetByID(ctx, *key.GroupID); gerr == nil && g != nil {
			platform = strings.TrimSpace(g.Platform)
		}
	}
	return key.GroupID, platform, nil
}

// groupPlatform resolves a group's platform; "" means unknown (not an error).
func (s *PelicanProbeService) groupPlatform(ctx context.Context, groupID int64) (string, error) {
	if s.judge.Groups == nil {
		return "", nil
	}
	g, err := s.judge.Groups.GetByID(ctx, groupID)
	if err != nil {
		return "", fmt.Errorf("look up group #%d: %w", groupID, err)
	}
	if g == nil {
		return "", nil
	}
	return strings.TrimSpace(g.Platform), nil
}

// validateCandyJudgeModel rejects candy plans whose judge model cannot be served
// by the group the judge call will actually be routed to. Catching this at
// create/update time is the whole point: an unroutable judge model otherwise
// fails silently on every run and leaves the verdict pending forever.
//
// judgeAPIKey is the plaintext judge key from the request ("" = the judge reuses
// the probe key). No upstream request is made — the check reuses the same
// persistent-eligibility diagnosis the request path uses to classify
// "no available accounts" as 404 model_not_found.
func (s *PelicanProbeService) validateCandyJudgeModel(ctx context.Context, probeGroupID int64, judgeModelID, judgeAPIKey string) error {
	judgeModelID = strings.TrimSpace(judgeModelID)
	if judgeModelID == "" || s.judge.Models == nil {
		return nil
	}

	groupID := probeGroupID
	platform := ""
	viaJudgeKey := strings.TrimSpace(judgeAPIKey) != ""
	if viaJudgeKey {
		keyGroupID, keyPlatform, err := s.resolveJudgeKeyGroup(ctx, judgeAPIKey)
		if err != nil {
			return err
		}
		if keyGroupID == nil {
			// Not bound to a group: routing depends on the owner's allowed
			// groups, which we cannot diagnose here. Stay inconclusive rather
			// than reject a configuration that may well work.
			return nil
		}
		groupID = *keyGroupID
		platform = keyPlatform
	} else {
		p, err := s.groupPlatform(ctx, probeGroupID)
		if err != nil {
			return err
		}
		platform = p
	}
	if platform == "" {
		// Unknown/composite platform: the effective platform is only resolved
		// per-model at request time. Stay inconclusive.
		return nil
	}

	diag := s.judge.Models.DiagnoseModelAvailabilityForPlatform(ctx, &groupID, judgeModelID, platform)
	if !diag.HasAccountsInPool || diag.HasModelSupport {
		return nil
	}
	if viaJudgeKey {
		return fmt.Errorf(
			"judge model %q is not available in the judge key's group (#%d); pick a model served by that group or provide a key bound to a group that serves it",
			judgeModelID, groupID,
		)
	}
	return fmt.Errorf(
		"judge model %q is not available in probe group (#%d); pick a model served by that group or provide a judge key bound to a group that serves it",
		judgeModelID, groupID,
	)
}
