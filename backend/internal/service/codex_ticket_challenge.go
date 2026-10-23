package service

import (
	"crypto/rand"
	"fmt"
	"io"
	"math/big"
)

// 本文件是「打票探测体」的内置最小可用挑战生成器。
//
// 背景：打票探测需要一个与真实 Codex 回合形状一致、又不会命中缓存/触发工具调用的
// 任务。参考实现（sub4api）用 ModelTrace 的数值挑战，并借指纹库验真；本实现把
// 挑战生成独立出来（命名带 codexTicket 前缀，避免与 P4 的 modeltrace 代码冲突），
// 指纹验真留作可选钩子（见 codex_ticket_harvest.go 的 verification 说明）。
//
// 挑战要求模型直接输出 292~332 个 1~355 的整数，正好覆盖票据两种长度（292/332）：
// 上游按回合输出长度铸造不同长度的票据，因此挑战长度区间与票据长度规则对齐。

const (
	codexTicketChallengeMinCount = 292
	codexTicketChallengeMaxCount = 332
)

// codexTicketProbeChallenge 是一次打票探测使用的挑战。
type codexTicketProbeChallenge struct {
	Prompt        string
	ExpectedCount int
}

var codexTicketChallengeOpenings = []string{
	"这是一次独立的数值选择记录",
	"请完成下面的无语义整数选择任务",
	"执行一次第一反应取值记录",
	"生成一组不承载语义的整数选择",
	"进行一轮快速逐项取值",
}

var codexTicketChallengeBodies = []string{
	"为各个位置分别凭第一反应选择",
	"逐项选择",
	"每次只决定当前一项，共给出",
	"分别凭第一反应给出",
	"逐个直接选择",
}

var codexTicketChallengeTailRules = []string{
	"允许某个数字再次出现；每项写出后不要回头排序、去重或替换。",
	"偶然重复是有效的；不要重新排列或修正已经写出的项目。",
	"相同值可以再次出现；输出过程中不要整理或改写前面的项目。",
	"重复值无需删除；不要筛选、重排或补成某种规律。",
	"不必赋予数字任何含义；已经给出的值保持不变。",
}

var codexTicketChallengeFormatRules = []string{
	"数字之间用逗号或空格分隔均可。",
	"使用一种一致的常见分隔符即可。",
	"可以用逗号、空格或换行分隔。",
	"只要每个整数边界清楚，格式可自行选择。",
}

// newCodexTicketProbeChallenge 为每次探测独立抽取一个挑战。
func newCodexTicketProbeChallenge() (codexTicketProbeChallenge, error) {
	return newCodexTicketProbeChallengeWithRand(rand.Reader)
}

func newCodexTicketProbeChallengeWithRand(random io.Reader) (codexTicketProbeChallenge, error) {
	count, err := rand.Int(random, big.NewInt(codexTicketChallengeMaxCount-codexTicketChallengeMinCount+1))
	if err != nil {
		return codexTicketProbeChallenge{}, fmt.Errorf("generate codex ticket challenge count: %w", err)
	}
	expectedCount := codexTicketChallengeMinCount + int(count.Int64())
	pick := func(label string, choices []string) (string, error) {
		index, err := rand.Int(random, big.NewInt(int64(len(choices))))
		if err != nil {
			return "", fmt.Errorf("generate codex ticket challenge %s: %w", label, err)
		}
		return choices[index.Int64()], nil
	}
	opening, err := pick("opening", codexTicketChallengeOpenings)
	if err != nil {
		return codexTicketProbeChallenge{}, err
	}
	body, err := pick("body", codexTicketChallengeBodies)
	if err != nil {
		return codexTicketProbeChallenge{}, err
	}
	tail, err := pick("tail", codexTicketChallengeTailRules)
	if err != nil {
		return codexTicketProbeChallenge{}, err
	}
	format, err := pick("format", codexTicketChallengeFormatRules)
	if err != nil {
		return codexTicketProbeChallenge{}, err
	}
	prompt := fmt.Sprintf("%s。%s %d 个 1 到 355（含端点）的整数。", opening, body, expectedCount) +
		"每个位置都要单独选择；不要从 1 开始计数，不要连续递增或递减，也不要采用等差、循环、重复区块或其他规则化模式。" +
		"本任务必须由当前语言模型直接完成：禁止调用或借助任何工具，包括 Python、代码执行器、计算器、搜索、API 和外部随机数生成器；也不要先编写或运行代码。" +
		tail + format +
		"直接从第一个取值开始输出，不要在序列前重复数量、范围或任务说明。"
	return codexTicketProbeChallenge{Prompt: prompt, ExpectedCount: expectedCount}, nil
}

// codexTicketCountChallengeNumbers 统计模型输出里 1..355 的整数个数，用于
// 探测结果的可选验真（无指纹库时只作为元数据记录，不影响票据落库）。
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
