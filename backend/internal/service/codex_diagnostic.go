package service

// CodexDiagnosticVerdict 是一次降智判定。
// Status: normal(正常) / degraded(疑似降智) / uncertain(不确定) / failed(请求失败)。
type CodexDiagnosticVerdict struct {
	Status         string
	Reason         string
	PredictedModel string
	Probability    float64
	ParsedCount    int
	// HTTPStatus 记录本次探测的上游状态码（0 表示未拿到响应）。
	HTTPStatus int
}

// EvaluateCodexDiagnostic 把探测输出与请求模型比对。
//
// 指纹库是闭集：请求的模型若不在库中（当前只有 gpt-6.1-sol），分类器永远
// 不会返回它的 ID。此时用同系列参考类兜底判定——命中参考类报 uncertain
// （库缺类，无法确认具体版本），命中其它任何 GPT 类才是 degraded。这样
// 健康的 6.1-sol 不会被误判成降智，而降级到 5.6/其它系列仍会被抓到。
func EvaluateCodexDiagnostic(model, output string, challenge ModelTraceChallenge) CodexDiagnosticVerdict {
	prediction, _, err := ModelTracePredictCommitted(output, challenge.ExpectedCount)
	verdict := CodexDiagnosticVerdict{Status: CodexDegradationStatusFailed, Reason: "insufficient_numbers", ParsedCount: prediction.ParsedCount}
	if err != nil {
		return verdict
	}
	verdict.PredictedModel = prediction.Model
	verdict.Probability = prediction.Probability
	if prediction.Model == model {
		return CodexDiagnosticVerdict{Status: CodexDegradationStatusNormal, PredictedModel: prediction.Model, Probability: prediction.Probability, ParsedCount: prediction.ParsedCount}
	}
	if reference, ok := modelTraceDiagnosticReferenceClass[model]; ok {
		if prediction.Model == reference {
			verdict.Status = CodexDegradationStatusUncertain
			verdict.Reason = "reference_class_unavailable"
			return verdict
		}
	}
	verdict.Status = CodexDegradationStatusDegraded
	verdict.Reason = "fingerprint_mismatch"
	return verdict
}
