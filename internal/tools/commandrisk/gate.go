package commandrisk

import (
	"fmt"
	"regexp"
	"strings"
)

type Decision uint8

const (
	DecisionAllow Decision = iota
	DecisionReflect
	DecisionRefuse
)

// Short aliases keep the verdict vocabulary readable at call sites.
const (
	Allow   = DecisionAllow
	Reflect = DecisionReflect
	Refuse  = DecisionRefuse
)

func (d Decision) String() string {
	switch d {
	case DecisionAllow:
		return "allow"
	case DecisionReflect:
		return "reflect"
	case DecisionRefuse:
		return "refuse"
	default:
		return "unknown"
	}
}

type Verdict struct {
	Decision Decision
	Message  string
}

var justificationPunctuation = regexp.MustCompile(`[^a-z0-9]+`)

// IsSubstantiveJustification intentionally rejects one-word confirmations and
// requires enough detail to identify an intentional, bounded action.
func IsSubstantiveJustification(value string) bool {
	trimmed := strings.TrimSpace(value)
	if len([]rune(trimmed)) < 25 {
		return false
	}
	normalized := strings.TrimSpace(justificationPunctuation.ReplaceAllString(strings.ToLower(trimmed), " "))
	switch normalized {
	case "yes", "y", "ok", "okay", "sure", "confirmed", "proceed", "do it", "continue", "approved", "go ahead":
		return false
	}
	return true
}

func reflectionPrompt(command string, assessment Assessment) string {
	return fmt.Sprintf(
		"Command held (refusal until justified): %s. The command was not started because %s. "+
			"Provide a substantive justification (at least 25 characters) that names the intended user request and the bounded target, then reissue the identical command with the justification field; a blind retry or a short confirmation will remain held.",
		command, assessment.Explanation(),
	)
}

// Gate converts an assessment into the execution decision. Catastrophic
// findings are an absolute refusal and cannot be overridden by justification.
func Gate(command string, assessment Assessment, justification string) Verdict {
	switch assessment.Level {
	case Catastrophic:
		return Verdict{
			Decision: DecisionRefuse,
			Message: fmt.Sprintf(
				"Command refused: %s. This target is catastrophic and cannot be confirmed; narrow the command to an explicitly bounded workspace path.",
				assessment.Explanation(),
			),
		}
	case Confirm:
		if !IsSubstantiveJustification(justification) {
			return Verdict{Decision: DecisionReflect, Message: reflectionPrompt(command, assessment)}
		}
	}
	return Verdict{Decision: DecisionAllow}
}

// Evaluate is a convenience for callers that have not already assessed the
// command.
func Evaluate(command string, ctx Context, justification string) (Assessment, Verdict) {
	assessment := Assess(command, ctx)
	return assessment, Gate(command, assessment, justification)
}
