package service

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// candyAnswerMarker is the literal prefix the candy prompt asks for.
const candyAnswerMarker = "ANSWER"

// candyAnswerParse is the deterministic parse of a candy answer's final line.
type candyAnswerParse struct {
	// Answer is the parsed number; only meaningful when Decided is true.
	Answer int
	// Found reports that at least one "ANSWER: <n>" line was present.
	Found bool
	// Conflict reports that the answer lines disagreed, so no single answer can
	// be trusted (fail closed instead of picking one).
	Conflict bool
}

// Decided reports whether the parse yields one unambiguous answer.
func (p candyAnswerParse) Decided() bool { return p.Found && !p.Conflict }

// reason renders a short phrase for the result note.
func (p candyAnswerParse) reason() string {
	switch {
	case p.Conflict:
		return "conflicting ANSWER lines"
	case !p.Found:
		return "no ANSWER line"
	default:
		return ""
	}
}

// parseCandyAnswer extracts the last standalone "ANSWER: <n>" line from a candy
// answer.
//
// The contract is fail-closed: a line only counts when it is exactly the marker,
// a colon (ASCII or full-width) and a decimal number, after stripping the
// markdown emphasis models wrap around it ("**ANSWER: 21**") and the optional
// unit ("ANSWER: 21 个"). Prose that merely mentions ANSWER, a placeholder
// ("ANSWER: <数字>") or a number lifted from the derivation never matches, and
// two disagreeing ANSWER lines mark the parse as conflicted instead of guessing
// which one is final. Anything undecided goes to the judge fallback, so a
// mis-parse can never silently produce a verdict.
func parseCandyAnswer(content string) candyAnswerParse {
	var out candyAnswerParse
	for _, raw := range strings.Split(content, "\n") {
		value, ok := parseCandyAnswerLine(raw)
		if !ok {
			continue
		}
		if out.Found && out.Answer != value {
			out.Conflict = true
			continue
		}
		out.Answer = value
		out.Found = true
	}
	return out
}

// parseCandyAnswerLine parses one line as "ANSWER: <n>".
func parseCandyAnswerLine(raw string) (int, bool) {
	line := strings.TrimSpace(raw)
	// Models decorate the line as a quote, a list item or bold text.
	line = strings.TrimLeft(line, ">*-#`~_ \t")
	line = strings.TrimSpace(line)
	line = strings.TrimRight(line, "*_`~ ")
	line = strings.TrimSpace(line)
	if len(line) < len(candyAnswerMarker) ||
		!strings.EqualFold(line[:len(candyAnswerMarker)], candyAnswerMarker) {
		return 0, false
	}
	rest := strings.TrimSpace(line[len(candyAnswerMarker):])
	switch {
	case strings.HasPrefix(rest, ":"):
		rest = rest[1:]
	case strings.HasPrefix(rest, "："):
		rest = strings.TrimPrefix(rest, "：")
	default:
		return 0, false
	}
	rest = strings.Trim(rest, "*_`~ ")
	rest = strings.TrimSpace(rest)
	// Trailing punctuation / unit models append ("21。", "21 个", "21)").
	for {
		trimmed := strings.TrimRight(rest, "。.，,、!！;；)） ")
		trimmed = strings.TrimSuffix(trimmed, "个")
		trimmed = strings.TrimSuffix(trimmed, "颗")
		trimmed = strings.TrimSuffix(trimmed, "粒")
		trimmed = strings.TrimSpace(trimmed)
		if trimmed == rest {
			break
		}
		rest = trimmed
	}
	if rest == "" || len(rest) > 4 {
		return 0, false
	}
	for _, r := range rest {
		if r < '0' || r > '9' {
			return 0, false
		}
	}
	value, err := strconv.Atoi(rest)
	if err != nil {
		return 0, false
	}
	return value, true
}

// applyCandyVerdict grades a successful candy probe.
//
// Primary path: parse the "ANSWER: <n>" line and compare it with
// PelicanCandyExpectedAnswer — deterministic, free (no second upstream call) and
// reproducible. The LLM judge is only a fallback for answers that ignored the
// contract or contradicted themselves; a judge that cannot decide leaves the
// verdict pending for manual review, and a failed probe never reaches here.
func (s *PelicanProbeService) applyCandyVerdict(ctx context.Context, row *PelicanProbePlanRow, result *PelicanProbeResult, content string) {
	parsed := parseCandyAnswer(content)
	if parsed.Decided() {
		answer := parsed.Answer
		result.AnswerValue = &answer
		if answer == PelicanCandyExpectedAnswer {
			result.Verdict = PelicanVerdictPass
		} else {
			result.Verdict = PelicanVerdictDegraded
		}
		now := time.Now()
		result.ReviewedBy = PelicanAutoReviewer
		result.ReviewedAt = &now
		result.Note = fmt.Sprintf("auto-parse: answer=%d expected=%d (prompt v%d)",
			answer, PelicanCandyExpectedAnswer, PelicanCandyPromptVersion)
		return
	}
	if strings.TrimSpace(row.JudgeModelID) == "" {
		result.Note = fmt.Sprintf("auto-parse: %s; auto-judge skipped: judge_model_id not configured (awaiting manual review)", parsed.reason())
		return
	}
	s.applyCandyJudge(ctx, row, result, content)
	// Keep the judge note first when it decided: publicResultNote only exposes
	// notes that start with "judge=", and the parse failure is admin detail.
	if result.Verdict == PelicanVerdictPass || result.Verdict == PelicanVerdictDegraded {
		result.Note = strings.TrimSpace(result.Note + " | auto-parse: " + parsed.reason())
		return
	}
	result.Note = "auto-parse: " + parsed.reason() + "; " + strings.TrimSpace(result.Note)
}
