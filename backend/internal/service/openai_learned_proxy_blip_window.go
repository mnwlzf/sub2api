package service

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"go.uber.org/zap"
)

// 学习型代理抖动窗口（learned proxy blip window）——本文件持有它的常量、按 proxyID
// 状态与抑制判定。
//
// 缺口（PR #29 的遗留）：opencode_warp_rotate.go 的「轮换预期窗口」只覆盖 sub2api
// 自己发起的出口轮换——窗口在调用轮换服务之前开启，因此只有本进程知道的那次轮换
// 受保护。宿主机的 systemd warp-rotate.timer（每 10 分钟跑一次 maintain，probe-first，
// 只有自己探测到 429 才轮换）会重启同一个 warp-test 容器，这条路径对 sub2api
// 完全不可见：窗口不会打开，同样的 4–8 秒隧道中断（DNS `no such host` /
// `connection refused`）依旧按 classifyUpstreamTransportError 的持久语义把该代理上的
// 全部账号写进 10 分钟 temp_unschedulable 冷却，路由层无可用账号 → 整组停摆。
//
// 思路：不再依赖「我知道我要轮换了」，而是**从错误形状反推**「这是代理层瞬时抖动」。
// 触发条件三者同时成立：
//  1. 该账号配了代理（account.ProxyID != nil，且 > 0）；
//  2. 错误已被判为 Persistent（否则本来就不会冷却，无需宽限）；
//  3. 错误形状是**代理路径失败**：DNS not-found（*net.DNSError.IsNotFound）或连接类
//     （ECONNREFUSED / EHOSTUNREACH / ENETUNREACH）。
//
// 行为（见 observeLearnedBlipFailure）：
//   - 该 proxyID 当前有活跃窗口 → 抑制冷却，**不重复开窗**；
//   - 没有活跃窗口，且距上次开窗已超过 openAILearnedBlipMinInterval → 开一个
//     openAILearnedBlipWindow 宽的窗口并抑制；
//   - 否则（窗口刚用掉、最小间隔未过）→ 照常冷却。这是「真坏代理」的收敛路径：
//     给了宽限仍持续失败就必须冷却。
//
// 边界：一个持续死亡的代理每个最小间隔最多被宽限一次（30s），之后照常冷却，因此
// 抑制不可能无限延长；未配代理的直连账号完全不进入本逻辑；本轮改动不触碰
// classifyUpstreamTransportError 的全局语义。
const (
	// openAILearnedBlipWindow 是学习型宽限窗口的宽度。
	//
	// 取值理由：宿主机 systemd 轮换造成的隧道中断是 4–8 秒（出口容器重启 + 隧道
	// 重建），30 秒足以覆盖「中断期 + 恢复瞬间的余抖」，同时远小于 10 分钟冷却，
	// 不会把一个真故障代理长期留在调度池里。
	openAILearnedBlipWindow = 30 * time.Second

	// openAILearnedBlipMinInterval 是同一 proxyID 两次开窗之间的最小间隔。
	//
	// 取值理由：宿主机 timer 周期是 10 分钟，5 分钟的最小间隔保证一次真实轮换抖动
	// 一定拿得到宽限（相邻两次抖动不会互相吃掉额度），而一个持续失败的代理在
	// 30 秒宽限用完后立刻回到冷却路径——最多只被宽限一次就被摘出调度。
	openAILearnedBlipMinInterval = 5 * time.Minute

	// openAILearnedBlipSuppressReason 是抑制 temp-unschedule 时写入日志的 reason 值。
	// 与轮换窗口的 openCodeWarpBlipSuppressReason（warp_rotation_window）并列，
	// 运维可据此区分「我们自己的轮换」与「学习到的代理层抖动」。
	openAILearnedBlipSuppressReason = "learned_proxy_blip_window"
)

// openAIProxyPathFailureMarkers 是「代理路径失败」的字符串兜底标记。
//
// 只在 typed 检查（syscall 常量、*net.DNSError）都不命中时使用，语义与
// classifyUpstreamTransportError 的网络类标记保持一致（生产实证两条错误串即
// 通过这里命中——x/net/proxy 的 SOCKS 拨号错误在部分实现里以裸字符串返回）。
//
// 刻意**不包含** "authentication failed" / "proxy authentication required"：
// 代理凭据被拒是配置型持久故障，不是轮换抖动，绝不能用学习窗口宽限。
var openAIProxyPathFailureMarkers = []string{
	"connection refused",
	"no route to host",
	"network is unreachable",
	"no such host",
}

// isOpenAIProxyPathTransportErrorShape 判定一个传输层错误是否呈「代理路径失败」形状。
//
// 这是 classifyUpstreamTransportError「持久」语义的一个**子集**（仅网络/路由类，
// 不含代理凭据类）。与 classifier 故意保留少量重复：classifier 是已上线的热路径
// 判定，改它会影响全局语义；这里只做「像不像轮换造成的抖动」的窄判定。
func isOpenAIProxyPathTransportErrorShape(err error) bool {
	if err == nil {
		return false
	}

	// — Typed 检查（首选，与 classifier 同源）———————————————————————————
	if errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.EHOSTUNREACH) ||
		errors.Is(err, syscall.ENETUNREACH) {
		return true
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
		return true
	}

	// — 字符串兜底 ————————————————————————————————————————————————
	msg := strings.ToLower(err.Error())
	for _, marker := range openAIProxyPathFailureMarkers {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

// learnedBlipOutcome 描述一次学习窗口判定的结果，直接用于日志字段值。
type learnedBlipOutcome string

const (
	// learnedBlipOutcomeCoolDown：不宽限，调用方照常冷却。
	learnedBlipOutcomeCoolDown learnedBlipOutcome = "cool_down"
	// learnedBlipOutcomeActiveWindow：命中已活跃的学习窗口（复用，不重复开窗）。
	learnedBlipOutcomeActiveWindow learnedBlipOutcome = "active_window"
	// learnedBlipOutcomeOpenedWindow：本次新开了一个学习窗口。
	learnedBlipOutcomeOpenedWindow learnedBlipOutcome = "opened_window"
)

// openAILearnedBlipState 持有学习型代理抖动窗口的按 proxyID 状态与观测计数。
//
// 与 openCodeWarpRotateState 刻意分离：那边的 blipUntil 表达「我们知道自己在轮换」，
// 这里的 learnedBlipUntil 表达「我们从错误形状反推这是代理层抖动」，两者语义不同，
// 混用一个 map 会让「窗口为什么开着」无法解释，也会让轮换侧的收敛逻辑误伤学习侧。
// 两个状态各自一把 mu（不与 openCodeWarpRotateMu 嵌套，避免锁序问题）。
type openAILearnedBlipState struct {
	mu sync.Mutex
	// learnedBlipUntil 按 proxyID 记录学习窗口的截止时间。条目数受代理数量约束。
	learnedBlipUntil map[int64]time.Time
	// learnedBlipOpenedAt 按 proxyID 记录上一次**开窗**时刻，用于最小间隔判定。
	// 以开窗时刻（而不是窗口结束时刻）计时：窗口内的重复失败不会顺延额度。
	learnedBlipOpenedAt map[int64]time.Time

	// learnedBlipOpened 是累计开窗次数；learnedBlipUsed 是累计抑制冷却的次数
	// （含开窗当次与窗口内的后续命中）。两者都受 mu 保护。
	learnedBlipOpened int64
	learnedBlipUsed   int64
}

// observeLearnedBlipFailure 是学习窗口的状态机：给定一次「代理路径形状」的持久失败，
// 返回（窗口截止时间，判定结果）。判定结果不是 cool_down 时，调用方应抑制冷却。
//
// 状态转移（window 为窗口宽度，minInterval 为最小间隔）：
//
//	无窗口 / 窗口已过期 + 距上次开窗 ≥ minInterval → 开窗（记 openedAt = now，opened++）
//	有活跃窗口                                    → 复用（不延长、不重复开窗）
//	无窗口 / 窗口已过期 + 距上次开窗 <  minInterval → cool_down（不给宽限）
//
// 结果导向：一个持续死亡的代理最多每个 minInterval 拿到一次 window 宽度（30s）的
// 宽限，之后必定回到冷却路径，因此不存在「无限抑制」。
func (st *openAILearnedBlipState) observeLearnedBlipFailure(proxyID int64, now time.Time, window, minInterval time.Duration) (time.Time, learnedBlipOutcome) {
	if st == nil || proxyID <= 0 || window <= 0 {
		return time.Time{}, learnedBlipOutcomeCoolDown
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.learnedBlipUntil == nil {
		st.learnedBlipUntil = make(map[int64]time.Time)
	}
	if st.learnedBlipOpenedAt == nil {
		st.learnedBlipOpenedAt = make(map[int64]time.Time)
	}
	// 1) 活跃窗口：抑制，且不重复开窗（也就不会刷新最小间隔额度）。
	if until, ok := st.learnedBlipUntil[proxyID]; ok && now.Before(until) {
		st.learnedBlipUsed++
		return until, learnedBlipOutcomeActiveWindow
	}
	// 2) 额度间隔：窗口刚用掉 → 不给第二次宽限（真坏代理的收敛路径）。
	if minInterval > 0 {
		if last, ok := st.learnedBlipOpenedAt[proxyID]; ok && now.Sub(last) < minInterval {
			return time.Time{}, learnedBlipOutcomeCoolDown
		}
	}
	// 3) 开窗。
	until := now.Add(window)
	st.learnedBlipUntil[proxyID] = until
	st.learnedBlipOpenedAt[proxyID] = now
	st.learnedBlipOpened++
	st.learnedBlipUsed++
	return until, learnedBlipOutcomeOpenedWindow
}

// learnedBlipCounters 返回累计开窗 / 抑制次数（nil 安全）。
func (st *openAILearnedBlipState) learnedBlipCounters() (opened, used int64) {
	if st == nil {
		return 0, 0
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.learnedBlipOpened, st.learnedBlipUsed
}

// openAILearnedBlipState 惰性创建（并发安全）学习窗口状态。
func (s *OpenAIGatewayService) openAILearnedBlipState() *openAILearnedBlipState {
	if s == nil {
		return nil
	}
	s.openAILearnedBlipMu.Lock()
	defer s.openAILearnedBlipMu.Unlock()
	if s.openAILearnedBlip == nil {
		s.openAILearnedBlip = &openAILearnedBlipState{
			learnedBlipUntil:    make(map[int64]time.Time),
			learnedBlipOpenedAt: make(map[int64]time.Time),
		}
	}
	return s.openAILearnedBlip
}

// suppressOpenAITransportTempUnschedDuringLearnedProxyBlip 判定一次持久类传输错误是否
// 落在**学习型代理抖动窗口**内；是则记录稳定的抑制事件并返回 true，调用方跳过
// temp-unschedule（不写 DB、不设内存 block），错误事件与 failover 照常发生。
//
// 调用点：openai_upstream_transport_error.go 的 Persistent 分支，且只在
// suppressOpenAITransportTempUnschedDuringWarpRotation 未命中后调用——轮换窗口更精确
// （我们确知自己在轮换），学习窗口是兜底；轮换窗口活跃时不在这里重复开学习窗口。
//
// 副作用纪律：形状判定通过之前绝不创建状态，因此直连账号、代理凭据被拒等
// 「非代理路径」的持久故障在这条路径上完全零副作用。
func (s *OpenAIGatewayService) suppressOpenAITransportTempUnschedDuringLearnedProxyBlip(ctx context.Context, account *Account, err error, safeErr string) bool {
	if s == nil || account == nil || account.ProxyID == nil {
		return false
	}
	proxyID := *account.ProxyID
	if proxyID <= 0 {
		return false
	}
	if !isOpenAIProxyPathTransportErrorShape(err) {
		return false
	}
	state := s.openAILearnedBlipState()
	if state == nil {
		return false
	}

	until, outcome := state.observeLearnedBlipFailure(proxyID, time.Now(), openAILearnedBlipWindow, openAILearnedBlipMinInterval)
	if outcome == learnedBlipOutcomeCoolDown {
		// 距上次开窗未过最小间隔（真坏代理的收敛路径）：照常冷却，不记抑制事件。
		return false
	}
	opened, used := state.learnedBlipCounters()

	logger.FromContext(ctx).With(zap.String("component", "service.openai_gateway")).Warn(
		"openai.account_temp_unscheduled_transport_suppressed",
		zap.Int64("account_id", account.ID),
		zap.String("account_name", account.Name),
		zap.String("platform", account.Platform),
		zap.Int64("proxy_id", proxyID),
		zap.String("reason", openAILearnedBlipSuppressReason),
		zap.Time("blip_until", until),
		zap.String("learned_blip_outcome", string(outcome)),
		// 观测计数：opened=累计开窗次数，used=累计抑制次数（含开窗当次）。
		zap.Int64("learned_blip_opened", opened),
		zap.Int64("learned_blip_used", used),
		zap.String("upstream_error", safeErr),
	)
	return true
}
