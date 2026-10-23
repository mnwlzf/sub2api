package service

import (
	"crypto/rand"
	"fmt"
	"io"
)

// 打票探测挑战与 P4a 降智检测同源：复用 ModelTrace 的模板与长度区间。
//
// 这样做的原因：指纹库（modeltrace_unified_bank.json）是用 ModelTrace 的挑战
// 模板构建的，打票探测若用另一套措辞，输出分布会偏离指纹库，验真时容易把
// 健康模型误判成降智。统一模板后，打票结果可以直接用 EvaluateCodexDiagnostic
// 做模型指纹验真，不需要再维护第二套随机数协议。
//
// 挑战要求模型输出 292~332 个 1~355 的整数，正好覆盖票据两种长度（292/332）：
// 上游按回合输出长度铸造不同长度的票据，因此挑战长度区间与票据长度规则对齐。
const (
	codexTicketChallengeMinCount = modelTraceChallengeMinCount
	codexTicketChallengeMaxCount = modelTraceChallengeMaxCount
)

// codexTicketProbeChallenge 是一次打票探测使用的挑战。
type codexTicketProbeChallenge struct {
	Prompt        string
	ExpectedCount int
}

func newCodexTicketProbeChallenge() (codexTicketProbeChallenge, error) {
	return newCodexTicketProbeChallengeWithRand(rand.Reader)
}

func newCodexTicketProbeChallengeWithRand(random io.Reader) (codexTicketProbeChallenge, error) {
	challenge, err := newModelTraceChallenge(random)
	if err != nil {
		return codexTicketProbeChallenge{}, fmt.Errorf("generate codex ticket challenge: %w", err)
	}
	return codexTicketProbeChallenge{
		Prompt:        challenge.Prompt,
		ExpectedCount: challenge.ExpectedCount,
	}, nil
}

// codexTicketCountChallengeNumbers 统计模型输出里 1..355 的整数个数。
// 与指纹验真并行保留：指纹无法解析时用它记录探测体质量（落库为元数据）。
func codexTicketCountChallengeNumbers(output string) int {
	count := 0
	start := -1
	for i := 0; i <= len(output); i++ {
		var b byte
		if i < len(output) {
			b = output[i]
		}
		if b >= '0' && b <= '9' {
			if start < 0 {
				start = i
			}
			continue
		}
		if start >= 0 {
			if i-start <= 3 {
				value := 0
				for _, digit := range output[start:i] {
					value = value*10 + int(digit-'0')
				}
				if value >= 1 && value <= 355 {
					count++
				}
			}
			start = -1
		}
	}
	return count
}
