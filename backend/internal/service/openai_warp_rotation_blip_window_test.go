//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

// ---------------------------------------------------------------------------
// 生产事故复现用的两条真实错误串（21:13:25 起的 4–6 秒隧道中断期）
// ---------------------------------------------------------------------------

const (
	// Docker 内嵌 DNS（127.0.0.11）在出口容器重启期间查不到容器名。
	warpBlipDNSNotFoundErr = `socks connect tcp warp-test:1080->opencode.ai:443: dial tcp: lookup warp-test on 127.0.0.11:53: no such host`
	// 容器端口尚未就绪。
	warpBlipConnRefusedErr = `dial tcp 172.19.0.5:1080: connect: connection refused`
	// 代理凭据被拒：Persistent，但**不是**代理路径形状（见
	// isOpenAIProxyPathTransportErrorShape）。学习型代理抖动窗口刻意不覆盖这一类配置型
	// 持久故障，因此「轮换窗口之外照常冷却」的用例改用它，避免与学习窗口叠加。
	warpBlipProxyAuthFailedErr = `Post "https://chatgpt.com/backend-api/codex/responses": socks connect tcp 85.255.176.68:12324->chatgpt.com:443: username/password authentication failed`
)

// ---------------------------------------------------------------------------
// 测试替身
// ---------------------------------------------------------------------------

// warpBlipRepoStub 是并发安全的 SetTempUnschedulable 记录器。嵌入（nil 的）
// AccountRepository 接口，任何其他方法调用都会 panic —— 被测路径只能碰这一个方法。
type warpBlipRepoStub struct {
	AccountRepository

	mu    sync.Mutex
	calls []tempUnschedCall
}

func (r *warpBlipRepoStub) SetTempUnschedulable(_ context.Context, id int64, until time.Time, reason string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, tempUnschedCall{accountID: id, until: until, reason: reason})
	return nil
}

func (r *warpBlipRepoStub) snapshot() []tempUnschedCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]tempUnschedCall, len(r.calls))
	copy(out, r.calls)
	return out
}

func warpBlipAccount(id, proxyID int64, platform string) *Account {
	pid := proxyID
	return &Account{
		ID:       id,
		Name:     fmt.Sprintf("oc-%d", id),
		Platform: platform,
		ProxyID:  &pid,
		Proxy:    &Proxy{ID: proxyID, Name: "warp-test", Protocol: "socks5h", Host: "warp-test", Port: 1080},
	}
}

// warpBlipObservedContext 返回一个把 logger.FromContext 指向内存 observer 的 ctx，
// 用于断言抑制事件确实被记录。
func warpBlipObservedContext(t *testing.T) (context.Context, *observer.ObservedLogs) {
	t.Helper()
	core, logs := observer.New(zap.WarnLevel)
	return logger.IntoContext(context.Background(), zap.New(core)), logs
}

// warpWindowProbeRotator 在 EnsureFreshExit 执行期间探测轮换预期窗口是否已经打开，
// 并可注入 onCall 回调模拟「轮换进行中到达的传输错误」。
type warpWindowProbeRotator struct {
	svc     *OpenAIGatewayService
	account *Account

	mu       sync.Mutex
	calls    int
	observed time.Time
	onCall   func()
	err      error
}

func (r *warpWindowProbeRotator) EnsureFreshExit(_ context.Context, _ int64, _ string) (string, error) {
	r.mu.Lock()
	r.calls++
	r.observed = r.svc.openCodeWarpBlipWindowUntil(r.account)
	onCall := r.onCall
	err := r.err
	r.mu.Unlock()

	if onCall != nil {
		onCall()
	}
	if err != nil {
		return "", err
	}
	return "203.0.113.7", nil
}

func (r *warpWindowProbeRotator) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

// ---------------------------------------------------------------------------
// 前提：两条错误在全局分类下确实是「持久故障」——修复前正是它们触发 10 分钟冷却
// ---------------------------------------------------------------------------

func TestWarpRotationWindow_PremiseErrorsAreClassifiedPersistent(t *testing.T) {
	require.True(t, classifyUpstreamTransportError(errors.New(warpBlipDNSNotFoundErr)).Persistent,
		"DNS no such host 必须仍被判为持久信号（未改全局语义）")
	require.True(t, classifyUpstreamTransportError(errors.New(warpBlipConnRefusedErr)).Persistent,
		"connection refused 必须仍被判为持久信号（未改全局语义）")
}

// ---------------------------------------------------------------------------
// 1. 窗口内 → 抑制冷却（不写 DB、不设内存 block），但仍 failover + 记录错误事件
// ---------------------------------------------------------------------------

func TestWarpRotationWindow_SuppressesTempUnschedWhileActive(t *testing.T) {
	repo := &warpBlipRepoStub{}
	svc := &OpenAIGatewayService{accountRepo: repo}
	account := warpBlipAccount(31589, 17, PlatformOpenCodeGo)

	svc.openCodeWarpRotateState().openBlipWindow(17, time.Now(), openCodeWarpBlipHardCap)

	ctx, logs := warpBlipObservedContext(t)
	c, rec := newOpenAITransportErrTestContext()

	err := svc.handleOpenAIUpstreamTransportError(ctx, c, account, errors.New(warpBlipDNSNotFoundErr), false)

	// 仍然 failover（抑制只影响冷却，不影响故障切换）。
	var fo *UpstreamFailoverError
	require.True(t, errors.As(err, &fo), "窗口内的传输错误必须照常 failover")
	require.Equal(t, http.StatusBadGateway, fo.StatusCode)

	// 不写 DB。
	require.Empty(t, repo.snapshot(), "轮换预期窗口内不得写 temp-unschedulable 冷却")

	// 抑制事件可观测。
	entries := logs.FilterMessage("openai.account_temp_unscheduled_transport_suppressed").All()
	require.Len(t, entries, 1)
	fields := entries[0].ContextMap()
	require.Equal(t, int64(31589), fields["account_id"])
	require.Equal(t, int64(17), fields["proxy_id"])
	require.Equal(t, openCodeWarpBlipSuppressReason, fields["reason"])
	require.Equal(t, "warp_rotation_window", fields["reason"])
	require.Contains(t, fields["upstream_error"], "no such host")

	// 错误事件照常记录。
	rawEvents, ok := c.Get(OpsUpstreamErrorsKey)
	require.True(t, ok)
	events, ok := rawEvents.([]*OpsUpstreamErrorEvent)
	require.True(t, ok)
	require.Len(t, events, 1)
	require.Equal(t, "request_error", events[0].Kind)
	require.NotNil(t, events[0].ProxyID)
	require.Equal(t, int64(17), *events[0].ProxyID)

	require.Equal(t, 0, rec.Body.Len(), "service 不得写响应体，由 handler 负责")
}

// 同一代理下 PlatformOpenAI 账号（isOpenAIAccount=true）也必须被抑制：窗口是按
// proxyID 生效的，且此处能真正断言「内存 block 也没设」。
func TestWarpRotationWindow_SuppressesInMemoryBlockForSharedProxy(t *testing.T) {
	repo := &warpBlipRepoStub{}
	svc := &OpenAIGatewayService{accountRepo: repo}
	account := warpBlipAccount(4627, 17, PlatformOpenAI)

	svc.openCodeWarpRotateState().openBlipWindow(17, time.Now(), openCodeWarpBlipHardCap)

	c, _ := newOpenAITransportErrTestContext()
	err := svc.handleOpenAIUpstreamTransportError(context.Background(), c, account, errors.New(warpBlipConnRefusedErr), false)

	var fo *UpstreamFailoverError
	require.True(t, errors.As(err, &fo))
	require.Empty(t, repo.snapshot())
	require.False(t, svc.isOpenAIAccountRuntimeBlocked(account),
		"抑制路径不得设置内存 block")
}

// ---------------------------------------------------------------------------
// 2. 窗口外 / 窗口过期 / 非该 proxy / 无窗口 → 照常冷却（回归保护）
// ---------------------------------------------------------------------------

func TestWarpRotationWindow_ExpiredWindowStillTempUnschedules(t *testing.T) {
	repo := &warpBlipRepoStub{}
	svc := &OpenAIGatewayService{accountRepo: repo}
	account := warpBlipAccount(4627, 17, PlatformOpenAI)

	// 窗口起点在 3 分钟前 + 120s 硬上限 → 已过期。
	svc.openCodeWarpRotateState().openBlipWindow(17, time.Now().Add(-3*time.Minute), openCodeWarpBlipHardCap)
	require.True(t, svc.openCodeWarpBlipWindowUntil(account).IsZero(), "过期窗口必须报告为无窗口")

	ctx, logs := warpBlipObservedContext(t)
	c, _ := newOpenAITransportErrTestContext()
	// 用「非代理路径形状」的持久故障：本用例只验证轮换窗口过期后不再保护。
	// （代理路径形状的 DNS/refused 现在会被学习型代理抖动窗口兜底，见
	// openai_learned_proxy_blip_window_test.go。）
	err := svc.handleOpenAIUpstreamTransportError(ctx, c, account, errors.New(warpBlipProxyAuthFailedErr), false)

	var fo *UpstreamFailoverError
	require.True(t, errors.As(err, &fo))

	calls := repo.snapshot()
	require.Len(t, calls, 1, "窗口过期后必须照常冷却")
	require.Equal(t, int64(4627), calls[0].accountID)
	require.Contains(t, calls[0].reason, "authentication failed")
	require.True(t, svc.isOpenAIAccountRuntimeBlocked(account))
	require.Empty(t, logs.FilterMessage("openai.account_temp_unscheduled_transport_suppressed").All())
}

func TestWarpRotationWindow_OtherProxyAccountUnaffected(t *testing.T) {
	repo := &warpBlipRepoStub{}
	svc := &OpenAIGatewayService{accountRepo: repo}
	// 窗口开在 proxy 17 上，本账号绑定 proxy 99。
	svc.openCodeWarpRotateState().openBlipWindow(17, time.Now(), openCodeWarpBlipHardCap)
	account := warpBlipAccount(31568, 99, PlatformOpenCodeGo)

	c, _ := newOpenAITransportErrTestContext()
	// 同样用非代理路径形状：本用例验证的是「轮换窗口按 proxyID 生效」，
	// 不应被学习窗口（proxy 99 的首次代理路径失败会拿到自己的宽限）干扰。
	err := svc.handleOpenAIUpstreamTransportError(context.Background(), c, account, errors.New(warpBlipProxyAuthFailedErr), false)

	var fo *UpstreamFailoverError
	require.True(t, errors.As(err, &fo))
	calls := repo.snapshot()
	require.Len(t, calls, 1, "非该 proxy 的账号不得被抑制")
	require.Equal(t, int64(31568), calls[0].accountID)
}

func TestWarpRotationWindow_NoWindowStillTempUnschedules(t *testing.T) {
	repo := &warpBlipRepoStub{}
	svc := &OpenAIGatewayService{accountRepo: repo}
	account := warpBlipAccount(31588, 17, PlatformOpenCodeGo)

	c, _ := newOpenAITransportErrTestContext()
	// 非代理路径形状：无轮换窗口时保持原有冷却语义。代理路径形状（DNS no such host /
	// connection refused）在无轮换窗口时现在由学习型代理抖动窗口给一次 30s 宽限，
	// 其边界（过期 / 最小间隔 / 真坏代理）由 openai_learned_proxy_blip_window_test.go 覆盖。
	err := svc.handleOpenAIUpstreamTransportError(context.Background(), c, account, errors.New(warpBlipProxyAuthFailedErr), false)

	var fo *UpstreamFailoverError
	require.True(t, errors.As(err, &fo))
	require.Len(t, repo.snapshot(), 1, "从未轮换过的进程必须保持原有冷却语义")

	// 只读探测不得惰性创建轮换状态（未启用轮换的进程上传输错误路径零副作用）。
	require.Nil(t, svc.openCodeWarpRotate, "只读窗口探测不得创建轮换状态")
	require.Nil(t, svc.openAILearnedBlip, "形状不符时也不得创建学习窗口状态")
}

func TestWarpRotationWindow_NilAndDirectAccountsAreSafe(t *testing.T) {
	repo := &warpBlipRepoStub{}
	svc := &OpenAIGatewayService{accountRepo: repo}
	svc.openCodeWarpRotateState().openBlipWindow(17, time.Now(), openCodeWarpBlipHardCap)

	// 直连账号（无 ProxyID）。
	direct := &Account{ID: 900, Name: "direct", Platform: PlatformOpenAI}
	require.True(t, svc.openCodeWarpBlipWindowUntil(direct).IsZero())

	c, _ := newOpenAITransportErrTestContext()
	err := svc.handleOpenAIUpstreamTransportError(context.Background(), c, direct, errors.New(warpBlipConnRefusedErr), false)
	var fo *UpstreamFailoverError
	require.True(t, errors.As(err, &fo))
	require.Len(t, repo.snapshot(), 1)

	// nil 接收者 / nil 账号不 panic。
	var nilSvc *OpenAIGatewayService
	require.True(t, nilSvc.openCodeWarpBlipWindowUntil(direct).IsZero())
	require.True(t, svc.openCodeWarpBlipWindowUntil(nil).IsZero())
}

// ---------------------------------------------------------------------------
// 3. 窗口生命周期：只延长不缩短、收敛受硬上限约束、绝不永久张开
// ---------------------------------------------------------------------------

func TestWarpRotationBlipWindow_OpenExtendsCloseShrinks(t *testing.T) {
	state := &openCodeWarpRotateState{}
	t0 := time.Now()

	until := state.openBlipWindow(17, t0, openCodeWarpBlipHardCap)
	require.Equal(t, t0.Add(openCodeWarpBlipHardCap), until)

	// 并发/连续开启：更晚的窗口胜出，已有保护不被截断。
	until = state.openBlipWindow(17, t0.Add(10*time.Second), openCodeWarpBlipHardCap)
	require.Equal(t, t0.Add(10*time.Second).Add(openCodeWarpBlipHardCap), until)

	// 返回后收敛到 min(now+grace, hardCapUntil)。
	hardCapUntil := until
	until = state.closeBlipWindow(17, t0.Add(10*time.Second), openCodeWarpBlipGracePeriod, hardCapUntil)
	require.Equal(t, t0.Add(10*time.Second).Add(openCodeWarpBlipGracePeriod), until)

	// 收敛后：宽限期内仍生效，宽限期后失效。
	require.Equal(t, until, state.blipWindowUntil(17, t0.Add(34*time.Second)))
	require.True(t, state.blipWindowUntil(17, t0.Add(36*time.Second)).IsZero())

	// 收敛绝不延长：hardCapUntil 已过时，窗口不得被拉到 now+grace。
	shrunk := state.closeBlipWindow(17, t0.Add(time.Hour), openCodeWarpBlipGracePeriod, t0.Add(30*time.Second))
	require.Equal(t, t0.Add(30*time.Second), shrunk)
	require.True(t, state.blipWindowUntil(17, t0.Add(time.Hour)).IsZero())
}

func TestWarpRotationBlipWindow_HardCapBoundsRunawayCall(t *testing.T) {
	state := &openCodeWarpRotateState{}
	t0 := time.Now()
	hardCapUntil := state.openBlipWindow(17, t0, openCodeWarpBlipHardCap)

	// 调用 hang 住 8 分钟（远超硬上限）后返回：窗口必须已过期，而不是 now+grace。
	state.closeBlipWindow(17, t0.Add(8*time.Minute), openCodeWarpBlipGracePeriod, hardCapUntil)
	require.True(t, state.blipWindowUntil(17, t0.Add(8*time.Minute)).IsZero(),
		"硬上限必须防止窗口在异常路径上永久张开")
	require.True(t, state.blipWindowUntil(17, t0.Add(8*time.Minute+time.Hour)).IsZero())
}

// ---------------------------------------------------------------------------
// 4. 轮换链路：窗口在调用「之前」开启，调用返回后收敛
// ---------------------------------------------------------------------------

func TestWarpRotationWindow_OpenedBeforeRotationCallSuppressesInFlightTransportError(t *testing.T) {
	repo := &warpBlipRepoStub{}
	rotator := &warpWindowProbeRotator{}
	svc := openCodeWarpRotateTestService(openCodeWarpRotateTestRotateConfig(), nil, rotator)
	svc.accountRepo = repo
	account := openCodeWarpRotateTestAccount(PlatformOpenCodeGo)
	rotator.svc = svc
	rotator.account = account

	// 模拟「轮换进行中」到达的传输错误（生产日志里 21:13:25 那批）。
	var suppressedDuringCall bool
	rotator.onCall = func() {
		c, _ := newOpenAITransportErrTestContext()
		err := svc.handleOpenAIUpstreamTransportError(context.Background(), c, account, errors.New(warpBlipDNSNotFoundErr), false)
		var fo *UpstreamFailoverError
		suppressedDuringCall = errors.As(err, &fo) && len(repo.snapshot()) == 0
	}

	_, rotated := svc.rotateOpenCodeWarpExitOn429(
		context.Background(), openCodeWarpRotateTestContext(t, "/v1/responses"),
		account, http.StatusTooManyRequests, []byte(openCodeWarpRotateFreeUsageLimitBody),
	)

	require.True(t, rotated)
	require.Equal(t, 1, rotator.callCount())
	require.False(t, rotator.observed.IsZero(),
		"轮换调用进行中窗口必须已经打开（这是修复的关键：错误早于调用返回到达）")
	require.True(t, suppressedDuringCall, "轮换进行中的传输错误必须被抑制")

	// 调用返回后窗口收敛为「返回时刻 + 宽限期」，且不超过硬上限。
	until := svc.openCodeWarpBlipWindowUntil(account)
	require.False(t, until.IsZero())
	require.WithinDuration(t, time.Now().Add(openCodeWarpBlipGracePeriod), until, 5*time.Second)
	require.True(t, until.Before(time.Now().Add(openCodeWarpBlipHardCap)))
}

func TestWarpRotationWindow_MinIntervalSkipDoesNotOpenWindow(t *testing.T) {
	settings := openCodeWarpRotateTestRotateConfig()
	settings.MinIntervalSeconds = 3600
	rotator := &warpRotateFakeRotator{}
	svc := openCodeWarpRotateTestService(settings, nil, rotator)
	account := openCodeWarpRotateTestAccount(PlatformOpenCodeGo)
	proxyID := *account.ProxyID

	// 预置一次刚发生的轮换 → 本次命中最小间隔，不发起轮换调用。
	require.True(t, svc.openCodeWarpRotateState().reserve(proxyID, time.Hour, time.Now()))

	_, _, err := svc.ensureFreshWarpExit(context.Background(), settings, proxyID, "")
	require.ErrorIs(t, err, errOpenCodeWarpRotateMinInterval)
	require.Equal(t, 0, rotator.callCount())
	require.True(t, svc.openCodeWarpBlipWindowUntil(account).IsZero(),
		"未真正发起轮换时不得打开抑制窗口")
}

func TestWarpRotationWindow_FailedRotationStillClosesWindowWithinGrace(t *testing.T) {
	rotator := &warpRotateFakeRotator{err: errors.New("warp rotate service down")}
	svc := openCodeWarpRotateTestService(openCodeWarpRotateTestRotateConfig(), nil, rotator)
	account := openCodeWarpRotateTestAccount(PlatformOpenCodeGo)

	_, _, err := svc.ensureFreshWarpExit(context.Background(), openCodeWarpRotateTestRotateConfig(), *account.ProxyID, "")
	require.Error(t, err)

	until := svc.openCodeWarpBlipWindowUntil(account)
	require.False(t, until.IsZero())
	require.WithinDuration(t, time.Now().Add(openCodeWarpBlipGracePeriod), until, 5*time.Second,
		"失败返回也必须收敛到宽限期，而不是保持硬上限")
}

// ---------------------------------------------------------------------------
// 5. 并发安全：多账号 / 多请求同时命中窗口（配合 -race 运行）
// ---------------------------------------------------------------------------

func TestWarpRotationWindow_ConcurrentAccountsAndRequests(t *testing.T) {
	repo := &warpBlipRepoStub{}
	svc := &OpenAIGatewayService{accountRepo: repo}
	svc.openCodeWarpRotateState().openBlipWindow(17, time.Now(), openCodeWarpBlipHardCap)

	const perProxy = 16
	var wg sync.WaitGroup
	for i := 0; i < perProxy; i++ {
		// 同代理（窗口内）与其它代理（窗口外）交错并发命中。
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			account := warpBlipAccount(int64(1000+i), 17, PlatformOpenAI)
			_ = svc.handleOpenAIUpstreamTransportError(context.Background(), nil, account, errors.New(warpBlipConnRefusedErr), false)
		}(i)
		go func(i int) {
			defer wg.Done()
			account := warpBlipAccount(int64(2000+i), 99, PlatformOpenAI)
			// 非代理路径形状：窗口外的账号必须逐个照常冷却，不被学习型代理抖动窗口兜底。
			_ = svc.handleOpenAIUpstreamTransportError(context.Background(), nil, account, errors.New(warpBlipProxyAuthFailedErr), false)
		}(i)
	}
	// 同时并发读/开/收敛窗口状态本身。
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			state := svc.openCodeWarpRotateState()
			pid := int64(200 + i)
			hardCapUntil := state.openBlipWindow(pid, time.Now(), openCodeWarpBlipHardCap)
			_ = state.blipWindowUntil(pid, time.Now())
			state.closeBlipWindow(pid, time.Now(), openCodeWarpBlipGracePeriod, hardCapUntil)
		}(i)
	}
	wg.Wait()

	// 只有窗口外的 proxy 99 账号被冷却。
	calls := repo.snapshot()
	require.Len(t, calls, perProxy)
	for _, call := range calls {
		require.GreaterOrEqual(t, call.accountID, int64(2000))
		require.Less(t, call.accountID, int64(2000+perProxy))
	}
	require.False(t, svc.isOpenAIAccountRuntimeBlocked(warpBlipAccount(1000, 17, PlatformOpenAI)))
	require.True(t, svc.isOpenAIAccountRuntimeBlocked(warpBlipAccount(2000, 99, PlatformOpenAI)))
}

// 窗口状态自身的并发读写在 -race 下不得报数据竞争。
func TestWarpRotationBlipWindow_ConcurrentStateAccessIsRaceFree(t *testing.T) {
	state := &openCodeWarpRotateState{}
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(3)
		go func(i int) {
			defer wg.Done()
			state.openBlipWindow(int64(i%4), time.Now(), openCodeWarpBlipHardCap)
		}(i)
		go func(i int) {
			defer wg.Done()
			_ = state.blipWindowUntil(int64(i%4), time.Now())
		}(i)
		go func(i int) {
			defer wg.Done()
			state.closeBlipWindow(int64(i%4), time.Now(), openCodeWarpBlipGracePeriod, time.Now().Add(openCodeWarpBlipHardCap))
		}(i)
	}
	wg.Wait()
}
