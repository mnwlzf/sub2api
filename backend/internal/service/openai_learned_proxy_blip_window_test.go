//go:build unit

package service

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 学习型代理抖动窗口的测试。
//
// 复用的替身/常量来自 openai_warp_rotation_blip_window_test.go：
// warpBlipRepoStub（SetTempUnschedulable 记录器）、warpBlipAccount、
// warpBlipObservedContext、warpBlipDNSNotFoundErr / warpBlipConnRefusedErr
// （生产实证的两条原文）、newOpenAITransportErrTestContext。
//
// 覆盖重点（顺序与需求一致）：
//  1. 首次代理路径失败 → 抑制 + 开窗 + reason=learned_proxy_blip_window；
//  2. 窗口内再次失败 → 继续抑制、不重复开窗、不延长；
//  3. **窗口过期后仍失败 → 照常冷却**（核心回归保护）；
//  4. 窗口刚用掉（未过最小间隔）→ 不宽限，直接冷却；
//  5. 没配代理的账号 → 照常冷却，且零副作用；
//  6. Persistent 但非代理路径形状（代理凭据被拒）→ 照常冷却；
//  7. 与轮换窗口并存：轮换窗口优先，且不误开学习窗口；
//  8. 并发安全（多账号/多请求同时命中，每个 proxy 只开一次窗）。

// ---------------------------------------------------------------------------
// 测试用只读探针
//
// 生产代码刻意不为「读学习窗口 / 读计数」提供方法：它们只有断言用得到，而
// golangci-lint 默认不加载 unit-tagged 测试文件，仅被测试引用的方法会被 unused
// 判为未使用。因此探针放在测试文件里，且一律**只读**（不惰性创建状态）。
// ---------------------------------------------------------------------------

func learnedBlipStateForTest(svc *OpenAIGatewayService) *openAILearnedBlipState {
	if svc == nil {
		return nil
	}
	svc.openAILearnedBlipMu.Lock()
	defer svc.openAILearnedBlipMu.Unlock()
	return svc.openAILearnedBlip
}

// learnedBlipWindowUntilByProxyForTest 返回该 proxyID 当前生效的学习窗口；已过期返回零值。
func learnedBlipWindowUntilByProxyForTest(state *openAILearnedBlipState, proxyID int64, now time.Time) time.Time {
	if state == nil || proxyID <= 0 {
		return time.Time{}
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	until, ok := state.learnedBlipUntil[proxyID]
	if !ok || !now.Before(until) {
		return time.Time{}
	}
	return until
}

// learnedBlipWindowUntilForTest 返回该账号绑定代理当前生效的学习窗口。
func learnedBlipWindowUntilForTest(svc *OpenAIGatewayService, account *Account) time.Time {
	if account == nil || account.ProxyID == nil || *account.ProxyID <= 0 {
		return time.Time{}
	}
	return learnedBlipWindowUntilByProxyForTest(learnedBlipStateForTest(svc), *account.ProxyID, time.Now())
}

func learnedBlipCountersForTest(svc *OpenAIGatewayService) (opened, used int64) {
	return learnedBlipStateForTest(svc).learnedBlipCounters()
}

// ---------------------------------------------------------------------------
// 0. 形状判定：classifyUpstreamTransportError 语义不变，只是取其中「网络/路由」子集
// ---------------------------------------------------------------------------

func TestLearnedProxyBlip_ProxyPathShapeDetection(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		// 生产实证的两条原文（宿主机 timer 轮换造成的隧道中断期）。
		{"DNS no such host 原文", errors.New(warpBlipDNSNotFoundErr), true},
		{"connection refused 原文", errors.New(warpBlipConnRefusedErr), true},

		// typed 形状（classifier 的首选路径）。
		{"ECONNREFUSED bare", syscall.ECONNREFUSED, true},
		{"EHOSTUNREACH bare", syscall.EHOSTUNREACH, true},
		{"ENETUNREACH bare", syscall.ENETUNREACH, true},
		{"socks OpError 链里的 DNS IsNotFound", &net.OpError{
			Op:  "socks connect",
			Net: "tcp",
			Err: &net.OpError{Op: "dial", Net: "tcp", Err: &net.DNSError{
				Err: "no such host", Name: "warp-test", IsNotFound: true,
			}},
		}, true},
		{"no route to host", errors.New(`dial tcp 1.2.3.4:443: connect: no route to host`), true},
		{"network is unreachable", errors.New(`dial tcp 1.2.3.4:443: connect: network is unreachable`), true},

		// 持久但**不是**代理路径形状：不得给宽限。
		{"代理凭据被拒（配置型持久故障）", errors.New(warpBlipProxyAuthFailedErr), false},
		{"HTTP 代理 407", errors.New(`proxyconnect tcp: 407 Proxy Authentication Required`), false},
		{"DNS 超时（IsNotFound=false）", &net.DNSError{Err: "i/o timeout", Name: "warp-test", IsTimeout: true}, false},

		// 非持久类（本来就不会冷却，无需宽限）。
		{"connection reset by peer", errors.New(`read tcp 10.0.0.1:5->2.2.2.2:443: read: connection reset by peer`), false},
		{"i/o timeout", errors.New(`dial tcp 1.2.3.4:443: i/o timeout`), false},
		{"nil", nil, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, isOpenAIProxyPathTransportErrorShape(tc.err))
		})
	}

	// 前提：被判为「非代理路径」的凭据类错误在全局分类下**仍是** Persistent——
	// 本次改动没有放宽 classifyUpstreamTransportError 的语义，它照常冷却。
	require.True(t, classifyUpstreamTransportError(
		errors.New(`socks connect tcp 85.255.176.68:12324->chatgpt.com:443: username/password authentication failed`)).Persistent,
		"代理凭据被拒必须仍是持久故障（全局语义未改）")
	require.True(t, classifyUpstreamTransportError(errors.New(warpBlipDNSNotFoundErr)).Persistent,
		"DNS no such host 必须仍是持久故障（全局语义未改）")
}

// ---------------------------------------------------------------------------
// 1. 首次代理路径失败 → 抑制 + 开窗
// ---------------------------------------------------------------------------

func TestLearnedProxyBlip_FirstProxyPathFailureSuppressesAndOpensWindow(t *testing.T) {
	repo := &warpBlipRepoStub{}
	svc := &OpenAIGatewayService{accountRepo: repo}
	// PlatformOpenAI（isOpenAIAccount=true）→ 可同时断言「内存 block 也没设」。
	account := warpBlipAccount(31568, 17, PlatformOpenAI)
	ctx, logs := warpBlipObservedContext(t)
	c, rec := newOpenAITransportErrTestContext()

	before := time.Now()
	err := svc.handleOpenAIUpstreamTransportError(ctx, c, account, errors.New(warpBlipDNSNotFoundErr), false)
	after := time.Now()

	// 抑制只影响冷却：仍然 failover。
	var fo *UpstreamFailoverError
	require.True(t, errors.As(err, &fo), "学习窗口内的传输错误必须照常 failover")
	require.Equal(t, http.StatusBadGateway, fo.StatusCode)

	require.Empty(t, repo.snapshot(), "首次代理路径失败必须被学习窗口抑制（不写 DB）")
	require.False(t, svc.isOpenAIAccountRuntimeBlocked(account), "抑制路径不得设置内存 block")
	require.Equal(t, 0, rec.Body.Len(), "service 不得写响应体，由 handler 负责")

	entries := logs.FilterMessage("openai.account_temp_unscheduled_transport_suppressed").All()
	require.Len(t, entries, 1)
	fields := entries[0].ContextMap()
	require.Equal(t, "learned_proxy_blip_window", fields["reason"])
	require.Equal(t, openAILearnedBlipSuppressReason, fields["reason"])
	require.Equal(t, int64(17), fields["proxy_id"])
	require.Equal(t, int64(31568), fields["account_id"])
	require.Equal(t, PlatformOpenAI, fields["platform"])
	require.Contains(t, fields["upstream_error"], "no such host")
	require.Equal(t, string(learnedBlipOutcomeOpenedWindow), fields["learned_blip_outcome"])
	require.Equal(t, int64(1), fields["learned_blip_opened"])
	require.Equal(t, int64(1), fields["learned_blip_used"])

	until, ok := fields["blip_until"].(time.Time)
	require.True(t, ok, "blip_until 必须是时间字段")
	require.False(t, until.Before(before.Add(openAILearnedBlipWindow-time.Second)))
	require.False(t, until.After(after.Add(openAILearnedBlipWindow+time.Second)))
	require.WithinDuration(t, time.Now().Add(openAILearnedBlipWindow), until, 5*time.Second)
	require.False(t, learnedBlipWindowUntilForTest(svc, account).IsZero())

	opened, used := learnedBlipCountersForTest(svc)
	require.Equal(t, int64(1), opened)
	require.Equal(t, int64(1), used)

	// ops 错误事件照常记录（抑制只影响冷却）。
	rawEvents, ok := c.Get(OpsUpstreamErrorsKey)
	require.True(t, ok)
	events, ok := rawEvents.([]*OpsUpstreamErrorEvent)
	require.True(t, ok)
	require.Len(t, events, 1)
	require.Equal(t, "request_error", events[0].Kind)
	require.NotNil(t, events[0].ProxyID)
	require.Equal(t, int64(17), *events[0].ProxyID)
}

// ---------------------------------------------------------------------------
// 2. 窗口内再次失败 → 继续抑制，不重复开窗、不延长
// ---------------------------------------------------------------------------

func TestLearnedProxyBlip_FailuresInsideWindowReuseItWithoutOpeningNew(t *testing.T) {
	repo := &warpBlipRepoStub{}
	svc := &OpenAIGatewayService{accountRepo: repo}
	// 同一代理下的三个账号（生产事故里 31568/31588/31589 同组停摆）。
	accounts := []*Account{
		warpBlipAccount(31568, 17, PlatformOpenAI),
		warpBlipAccount(31588, 17, PlatformOpenAI),
		warpBlipAccount(31589, 17, PlatformOpenAI),
	}
	ctx, logs := warpBlipObservedContext(t)

	for i, account := range accounts {
		c, _ := newOpenAITransportErrTestContext()
		err := svc.handleOpenAIUpstreamTransportError(ctx, c, account, errors.New(warpBlipConnRefusedErr), false)
		var fo *UpstreamFailoverError
		require.True(t, errors.As(err, &fo), "第 %d 次失败必须照常 failover", i+1)
	}

	require.Empty(t, repo.snapshot(), "窗口内所有账号的失败都必须被抑制")

	entries := logs.FilterMessage("openai.account_temp_unscheduled_transport_suppressed").All()
	require.Len(t, entries, 3)
	require.Equal(t, string(learnedBlipOutcomeOpenedWindow), entries[0].ContextMap()["learned_blip_outcome"])
	require.Equal(t, string(learnedBlipOutcomeActiveWindow), entries[1].ContextMap()["learned_blip_outcome"])
	require.Equal(t, string(learnedBlipOutcomeActiveWindow), entries[2].ContextMap()["learned_blip_outcome"])
	for i, entry := range entries {
		require.Equal(t, openAILearnedBlipSuppressReason, entry.ContextMap()["reason"], "第 %d 条抑制事件", i+1)
		require.Equal(t, int64(17), entry.ContextMap()["proxy_id"])
	}

	// 后续失败不得延长窗口：三条事件里的 blip_until 必须完全相同。
	untilFirst, ok := entries[0].ContextMap()["blip_until"].(time.Time)
	require.True(t, ok)
	untilLast, ok := entries[2].ContextMap()["blip_until"].(time.Time)
	require.True(t, ok)
	require.Equal(t, untilFirst, untilLast, "复用窗口不得被后续失败顺延")

	opened, used := learnedBlipCountersForTest(svc)
	require.Equal(t, int64(1), opened, "窗口内重复失败不得重复开窗")
	require.Equal(t, int64(3), used, "三次失败都由同一个学习窗口抑制")
}

// ---------------------------------------------------------------------------
// 3. 核心回归保护：窗口过期后仍失败 → 照常冷却
// ---------------------------------------------------------------------------

func TestLearnedProxyBlip_ExpiredWindowStillTempUnschedules(t *testing.T) {
	repo := &warpBlipRepoStub{}
	svc := &OpenAIGatewayService{accountRepo: repo}
	account := warpBlipAccount(4627, 17, PlatformOpenAI)

	// 在「31 秒前」用真实窗口宽度开过一次窗 → 此刻窗口已过期。
	// （30s 窗口 < 5 分钟最小间隔，因此这也是「刚用掉额度」的场景；
	//  纯「窗口过期但额度已恢复」的路径由下面的状态机测试覆盖。）
	seedUntil, seedOutcome := svc.openAILearnedBlipState().observeLearnedBlipFailure(
		17, time.Now().Add(-openAILearnedBlipWindow-time.Second),
		openAILearnedBlipWindow, openAILearnedBlipMinInterval)
	require.Equal(t, learnedBlipOutcomeOpenedWindow, seedOutcome)
	require.True(t, seedUntil.Before(time.Now()), "预置窗口必须已过期")
	require.True(t, learnedBlipWindowUntilForTest(svc, account).IsZero(), "过期窗口必须报告为无窗口")

	ctx, logs := warpBlipObservedContext(t)
	c, _ := newOpenAITransportErrTestContext()
	err := svc.handleOpenAIUpstreamTransportError(ctx, c, account, errors.New(warpBlipConnRefusedErr), false)

	var fo *UpstreamFailoverError
	require.True(t, errors.As(err, &fo))

	// —— 核心断言：过期窗口不再保护，账号照常冷却 ——
	calls := repo.snapshot()
	require.Len(t, calls, 1, "窗口过期后必须照常冷却")
	require.Equal(t, int64(4627), calls[0].accountID)
	require.Contains(t, calls[0].reason, "connection refused")
	require.True(t, svc.isOpenAIAccountRuntimeBlocked(account), "过期后必须设置内存 block")
	require.Empty(t, logs.FilterMessage("openai.account_temp_unscheduled_transport_suppressed").All(),
		"不得再记录抑制事件")

	// 没有重复开窗：计数停留在预置时的 1/1。
	opened, used := learnedBlipCountersForTest(svc)
	require.Equal(t, int64(1), opened)
	require.Equal(t, int64(1), used)
}

// ---------------------------------------------------------------------------
// 4. 额度间隔：窗口刚用掉 → 不宽限直接冷却；到期后每次只给一个窗口
// ---------------------------------------------------------------------------

func TestLearnedProxyBlipState_MinIntervalGrantsAtMostOneWindowPerInterval(t *testing.T) {
	state := &openAILearnedBlipState{}
	t0 := time.Now()

	until, outcome := state.observeLearnedBlipFailure(17, t0, openAILearnedBlipWindow, openAILearnedBlipMinInterval)
	require.Equal(t, learnedBlipOutcomeOpenedWindow, outcome)
	require.Equal(t, t0.Add(openAILearnedBlipWindow), until)

	// 窗口内：复用（不重复开窗、不延长）。
	until2, outcome2 := state.observeLearnedBlipFailure(17, t0.Add(10*time.Second), openAILearnedBlipWindow, openAILearnedBlipMinInterval)
	require.Equal(t, learnedBlipOutcomeActiveWindow, outcome2)
	require.Equal(t, until, until2)

	// 窗口过期（+1s）但最小间隔未过：不宽限。
	expired := t0.Add(openAILearnedBlipWindow + time.Second)
	_, outcome3 := state.observeLearnedBlipFailure(17, expired, openAILearnedBlipWindow, openAILearnedBlipMinInterval)
	require.Equal(t, learnedBlipOutcomeCoolDown, outcome3, "窗口过期后不得自动续期")
	require.True(t, learnedBlipWindowUntilByProxyForTest(state, 17, expired).IsZero())

	// 距上次开窗 4 分 59 秒：仍然不宽限（额度未恢复）。
	almost := t0.Add(openAILearnedBlipMinInterval - time.Second)
	_, outcome4 := state.observeLearnedBlipFailure(17, almost, openAILearnedBlipWindow, openAILearnedBlipMinInterval)
	require.Equal(t, learnedBlipOutcomeCoolDown, outcome4, "最小间隔内绝不给第二次宽限")

	// 最小间隔到点：重新给一个窗口（额度是「每间隔一次」，不是一次性）。
	granted := t0.Add(openAILearnedBlipMinInterval + time.Second)
	until5, outcome5 := state.observeLearnedBlipFailure(17, granted, openAILearnedBlipWindow, openAILearnedBlipMinInterval)
	require.Equal(t, learnedBlipOutcomeOpenedWindow, outcome5)
	require.Equal(t, granted.Add(openAILearnedBlipWindow), until5)

	// 计数：两次开窗，三次抑制（开窗 1 + 窗口内 1 + 重新开窗 1）。
	opened, used := state.learnedBlipCounters()
	require.Equal(t, int64(2), opened)
	require.Equal(t, int64(3), used)

	// 另一个 proxyID 完全独立。
	require.True(t, learnedBlipWindowUntilByProxyForTest(state, 99, t0).IsZero())
	_, otherOutcome := state.observeLearnedBlipFailure(99, t0, openAILearnedBlipWindow, openAILearnedBlipMinInterval)
	require.Equal(t, learnedBlipOutcomeOpenedWindow, otherOutcome)
}

// 同一个「刚用掉窗口」场景走完整 handler：必须冷却，且不得重复开窗。
func TestLearnedProxyBlip_RecentlyConsumedWindowCoolsDownWithoutGrace(t *testing.T) {
	repo := &warpBlipRepoStub{}
	svc := &OpenAIGatewayService{accountRepo: repo}
	account := warpBlipAccount(31588, 17, PlatformOpenAI)

	// 4 分钟前开过窗（窗口 30s → 早已过期，但 5 分钟额度未恢复）。
	_, seedOutcome := svc.openAILearnedBlipState().observeLearnedBlipFailure(
		17, time.Now().Add(-4*time.Minute), openAILearnedBlipWindow, openAILearnedBlipMinInterval)
	require.Equal(t, learnedBlipOutcomeOpenedWindow, seedOutcome)

	ctx, logs := warpBlipObservedContext(t)
	c, _ := newOpenAITransportErrTestContext()
	err := svc.handleOpenAIUpstreamTransportError(ctx, c, account, errors.New(warpBlipDNSNotFoundErr), false)

	var fo *UpstreamFailoverError
	require.True(t, errors.As(err, &fo))

	calls := repo.snapshot()
	require.Len(t, calls, 1, "额度未恢复时必须直接冷却（真坏代理的收敛路径）")
	require.Contains(t, calls[0].reason, "no such host")
	require.Empty(t, logs.FilterMessage("openai.account_temp_unscheduled_transport_suppressed").All())
	opened, used := learnedBlipCountersForTest(svc)
	require.Equal(t, int64(1), opened, "不得重复开窗")
	require.Equal(t, int64(1), used)
}

// ---------------------------------------------------------------------------
// 5. 没配代理 / 绑定非法 → 照常冷却，且零副作用
// ---------------------------------------------------------------------------

func TestLearnedProxyBlip_DirectAccountStillTempUnschedules(t *testing.T) {
	repo := &warpBlipRepoStub{}
	svc := &OpenAIGatewayService{accountRepo: repo}
	ctx, logs := warpBlipObservedContext(t)

	// 直连账号（无 ProxyID）：错误串形状完全命中，但没有代理可抖动。
	direct := &Account{ID: 900, Name: "direct", Platform: PlatformOpenAI}
	c, _ := newOpenAITransportErrTestContext()
	err := svc.handleOpenAIUpstreamTransportError(ctx, c, direct, errors.New(warpBlipDNSNotFoundErr), false)
	var fo *UpstreamFailoverError
	require.True(t, errors.As(err, &fo))

	calls := repo.snapshot()
	require.Len(t, calls, 1, "没配代理的账号不受学习窗口影响，照常冷却")
	require.Equal(t, int64(900), calls[0].accountID)
	require.True(t, svc.isOpenAIAccountRuntimeBlocked(direct))
	require.Empty(t, logs.FilterMessage("openai.account_temp_unscheduled_transport_suppressed").All())
	require.Nil(t, svc.openAILearnedBlip, "直连账号不得创建学习窗口状态")
	opened, used := learnedBlipCountersForTest(svc)
	require.Zero(t, opened)
	require.Zero(t, used)

	// ProxyID 指向 0（非法绑定）同样是直连语义。
	zeroProxy := int64(0)
	invalid := &Account{ID: 901, Name: "zero-proxy", Platform: PlatformOpenAI, ProxyID: &zeroProxy}
	c2, _ := newOpenAITransportErrTestContext()
	err = svc.handleOpenAIUpstreamTransportError(ctx, c2, invalid, errors.New(warpBlipConnRefusedErr), false)
	require.True(t, errors.As(err, &fo))
	require.Len(t, repo.snapshot(), 2)
	require.Nil(t, svc.openAILearnedBlip, "非法 proxyID 不得创建学习窗口状态")
}

// ---------------------------------------------------------------------------
// 6. Persistent 但不是代理路径形状 → 照常冷却（不削弱真故障处理）
// ---------------------------------------------------------------------------

func TestLearnedProxyBlip_NonProxyPathShapeStillTempUnschedules(t *testing.T) {
	repo := &warpBlipRepoStub{}
	svc := &OpenAIGatewayService{accountRepo: repo}
	account := warpBlipAccount(4627, 17, PlatformOpenAI) // 配了代理
	ctx, logs := warpBlipObservedContext(t)

	// 代理凭据被拒：Persistent，但属于配置型持久故障——正是学习窗口**绝不能**宽限的
	// 那一类（否则过期订阅的代理会被反复调度，回到 classifyUpstreamTransportError
	// 当初要修的原始事故）。
	authErr := errors.New(warpBlipProxyAuthFailedErr)
	require.True(t, classifyUpstreamTransportError(authErr).Persistent)

	c, _ := newOpenAITransportErrTestContext()
	err := svc.handleOpenAIUpstreamTransportError(ctx, c, account, authErr, false)
	var fo *UpstreamFailoverError
	require.True(t, errors.As(err, &fo))

	calls := repo.snapshot()
	require.Len(t, calls, 1, "代理凭据被拒必须立刻冷却")
	require.Contains(t, calls[0].reason, "authentication failed")
	require.True(t, svc.isOpenAIAccountRuntimeBlocked(account))
	require.Empty(t, logs.FilterMessage("openai.account_temp_unscheduled_transport_suppressed").All())
	require.Nil(t, svc.openAILearnedBlip, "形状不符时不得创建学习窗口状态")

	// 非持久（transient）错误：本来就不冷却，也不得开窗。
	c2, _ := newOpenAITransportErrTestContext()
	err = svc.handleOpenAIUpstreamTransportError(ctx, c2, account, errors.New("dial tcp 1.2.3.4:443: i/o timeout"), false)
	require.True(t, errors.As(err, &fo))
	require.Len(t, repo.snapshot(), 1, "transient 错误既不冷却也不宽限")
	require.Nil(t, svc.openAILearnedBlip)
	opened, used := learnedBlipCountersForTest(svc)
	require.Zero(t, opened)
	require.Zero(t, used)
}

// ---------------------------------------------------------------------------
// 7. 与轮换窗口并存：轮换窗口更精确，优先且不误开学习窗口
// ---------------------------------------------------------------------------

func TestLearnedProxyBlip_RotationWindowTakesPrecedence(t *testing.T) {
	repo := &warpBlipRepoStub{}
	svc := &OpenAIGatewayService{accountRepo: repo}
	account := warpBlipAccount(31568, 17, PlatformOpenCodeGo)

	// 轮换预期窗口活跃（PR #29 的机制）。
	svc.openCodeWarpRotateState().openBlipWindow(17, time.Now(), openCodeWarpBlipHardCap)

	ctx, logs := warpBlipObservedContext(t)
	c, _ := newOpenAITransportErrTestContext()
	err := svc.handleOpenAIUpstreamTransportError(ctx, c, account, errors.New(warpBlipDNSNotFoundErr), false)

	var fo *UpstreamFailoverError
	require.True(t, errors.As(err, &fo))
	require.Empty(t, repo.snapshot())

	entries := logs.FilterMessage("openai.account_temp_unscheduled_transport_suppressed").All()
	require.Len(t, entries, 1)
	require.Equal(t, "warp_rotation_window", entries[0].ContextMap()["reason"],
		"轮换窗口更精确：命中时必须用它自己的 reason")
	require.Equal(t, openCodeWarpBlipSuppressReason, entries[0].ContextMap()["reason"])

	require.Nil(t, svc.openAILearnedBlip, "轮换窗口活跃时不得开学习窗口")
	opened, used := learnedBlipCountersForTest(svc)
	require.Zero(t, opened)
	require.Zero(t, used)
}

// 学习窗口只作用于自己的 proxyID：proxy 17 的活跃窗口不得保护 proxy 99。
// （注意：每个 proxy 的**首次**失败都会拿到自己的宽限，所以这里必须先把 proxy 99
// 的额度也用掉，才能把「跨 proxy 串味」与「99 自己的首次宽限」区分开。）
func TestLearnedProxyBlip_WindowIsScopedToItsProxy(t *testing.T) {
	repo := &warpBlipRepoStub{}
	svc := &OpenAIGatewayService{accountRepo: repo}
	state := svc.openAILearnedBlipState()
	now := time.Now()

	// proxy 17：活跃窗口。proxy 99：4 分钟前用掉额度（窗口已过期、间隔未到）。
	_, outcome17 := state.observeLearnedBlipFailure(17, now, openAILearnedBlipWindow, openAILearnedBlipMinInterval)
	require.Equal(t, learnedBlipOutcomeOpenedWindow, outcome17)
	_, outcome99 := state.observeLearnedBlipFailure(99, now.Add(-4*time.Minute), openAILearnedBlipWindow, openAILearnedBlipMinInterval)
	require.Equal(t, learnedBlipOutcomeOpenedWindow, outcome99)

	other := warpBlipAccount(31589, 99, PlatformOpenAI)
	c, _ := newOpenAITransportErrTestContext()
	err := svc.handleOpenAIUpstreamTransportError(context.Background(), c, other, errors.New(warpBlipDNSNotFoundErr), false)

	var fo *UpstreamFailoverError
	require.True(t, errors.As(err, &fo))
	calls := repo.snapshot()
	require.Len(t, calls, 1, "proxy 17 的活跃窗口不得跨 proxy 抑制 proxy 99 的失败")
	require.Equal(t, int64(31589), calls[0].accountID)
	require.True(t, svc.isOpenAIAccountRuntimeBlocked(other))

	// 对照组：同一次调用里，proxy 17 上的账号仍被自己的窗口抑制。
	onSeventeen := warpBlipAccount(31568, 17, PlatformOpenAI)
	c2, _ := newOpenAITransportErrTestContext()
	err = svc.handleOpenAIUpstreamTransportError(context.Background(), c2, onSeventeen, errors.New(warpBlipDNSNotFoundErr), false)
	require.True(t, errors.As(err, &fo))
	require.Len(t, repo.snapshot(), 1, "proxy 17 的窗口仍保护 proxy 17 上的账号")
}

// ---------------------------------------------------------------------------
// 8. 并发安全：多账号 / 多请求同时命中，每个 proxy 只开一次窗
// ---------------------------------------------------------------------------

func TestLearnedProxyBlip_ConcurrentFailuresOpenExactlyOneWindowPerProxy(t *testing.T) {
	repo := &warpBlipRepoStub{}
	svc := &OpenAIGatewayService{accountRepo: repo}

	const perProxy = 16
	var wg sync.WaitGroup
	for i := 0; i < perProxy; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			account := warpBlipAccount(int64(1000+i), 17, PlatformOpenAI)
			_ = svc.handleOpenAIUpstreamTransportError(context.Background(), nil, account, errors.New(warpBlipConnRefusedErr), false)
		}(i)
		go func(i int) {
			defer wg.Done()
			account := warpBlipAccount(int64(2000+i), 99, PlatformOpenAI)
			_ = svc.handleOpenAIUpstreamTransportError(context.Background(), nil, account, errors.New(warpBlipDNSNotFoundErr), false)
		}(i)
	}
	wg.Wait()

	require.Empty(t, repo.snapshot(),
		"并发抖动必须全部被抑制：每个 proxy 首个失败开窗，其余复用同一个窗口")
	opened, used := learnedBlipCountersForTest(svc)
	require.Equal(t, int64(2), opened, "开窗必须原子：每个 proxy 恰好一次")
	require.Equal(t, int64(2*perProxy), used)
	require.False(t, learnedBlipWindowUntilForTest(svc, warpBlipAccount(1000, 17, PlatformOpenAI)).IsZero())
	require.False(t, learnedBlipWindowUntilForTest(svc, warpBlipAccount(2000, 99, PlatformOpenAI)).IsZero())
}

func TestLearnedProxyBlipState_ConcurrentAccessIsRaceFree(t *testing.T) {
	state := &openAILearnedBlipState{}
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(3)
		go func(i int) {
			defer wg.Done()
			state.observeLearnedBlipFailure(int64(i%4), time.Now(), openAILearnedBlipWindow, openAILearnedBlipMinInterval)
		}(i)
		go func(i int) {
			defer wg.Done()
			_ = learnedBlipWindowUntilByProxyForTest(state, int64(i%4), time.Now())
		}(i)
		go func(i int) {
			defer wg.Done()
			_, _ = state.learnedBlipCounters()
		}(i)
	}
	wg.Wait()
}

// ---------------------------------------------------------------------------
// 9. nil 安全：nil service / nil account / nil ctx 都不得 panic，也不得建状态
// ---------------------------------------------------------------------------

func TestLearnedProxyBlip_NilServiceAndAccountAreSafe(t *testing.T) {
	account := warpBlipAccount(31568, 17, PlatformOpenAI)
	proxyErr := errors.New(warpBlipDNSNotFoundErr)

	var nilSvc *OpenAIGatewayService
	require.False(t, nilSvc.suppressOpenAITransportTempUnschedDuringLearnedProxyBlip(context.Background(), account, proxyErr, "x"))
	require.True(t, learnedBlipWindowUntilForTest(nilSvc, account).IsZero())
	require.Nil(t, nilSvc.openAILearnedBlipState())
	opened, used := learnedBlipCountersForTest(nilSvc)
	require.Zero(t, opened)
	require.Zero(t, used)

	svc := &OpenAIGatewayService{}
	require.False(t, svc.suppressOpenAITransportTempUnschedDuringLearnedProxyBlip(context.Background(), nil, proxyErr, "x"))
	require.False(t, svc.suppressOpenAITransportTempUnschedDuringLearnedProxyBlip(context.Background(), &Account{ID: 1}, proxyErr, "x"),
		"无代理账号不进入学习逻辑")
	require.Nil(t, svc.openAILearnedBlip, "以上调用都不得惰性创建学习窗口状态")
	require.True(t, learnedBlipWindowUntilForTest(svc, nil).IsZero())

	// nil ctx：logger.FromContext 对 nil ctx 安全（回落全局 logger），形状/代理命中时
	// 仍正常开窗并抑制，不得 panic。
	ctxSvc := &OpenAIGatewayService{}
	require.True(t, ctxSvc.suppressOpenAITransportTempUnschedDuringLearnedProxyBlip(nil, account, proxyErr, "x"))
	require.False(t, learnedBlipWindowUntilForTest(ctxSvc, account).IsZero())
}
