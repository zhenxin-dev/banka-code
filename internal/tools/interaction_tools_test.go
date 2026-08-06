package tools

import (
	"context"
	"strings"
	"testing"
)

func TestAskUserReturnsInteractiveAnswer(t *testing.T) {
	interaction := &stubInteraction{answer: "second"}
	result, err := NewAskUserTool().Execute(context.Background(), map[string]any{
		"question": "Which option?",
		"options":  []any{"first", "second"},
	}, Context{Interaction: interaction})
	if err != nil {
		t.Fatalf("AskUser returned error: %v", err)
	}
	if result.IsError || result.Content != "User answer: second" {
		t.Fatalf("unexpected result: %#v", result)
	}
	if interaction.question.Question != "Which option?" || len(interaction.question.Options) != 2 {
		t.Fatalf("unexpected question: %#v", interaction.question)
	}
}

func TestEscalatedBashRequiresApproval(t *testing.T) {
	interaction := &stubInteraction{decision: ApprovalDeny}
	result, err := NewBashTool().Execute(context.Background(), map[string]any{
		"command":             "printf denied",
		"sandbox_permissions": "require_escalated",
		"justification":       "Needs network access",
	}, Context{WorkspaceRoot: t.TempDir(), Interaction: interaction})
	if err != nil {
		t.Fatalf("Bash returned error: %v", err)
	}
	if !result.IsError || !strings.Contains(result.Content, "denied") {
		t.Fatalf("unexpected denied result: %#v", result)
	}
	if interaction.approval.Command != "printf denied" || interaction.approval.Justification != "Needs network access" {
		t.Fatalf("unexpected approval request: %#v", interaction.approval)
	}
}

func TestEscalatedBashRunsAfterApproval(t *testing.T) {
	interaction := &stubInteraction{decision: ApprovalAllowOnce}
	result, err := NewBashTool().Execute(context.Background(), map[string]any{
		"command":             "printf approved",
		"sandbox_permissions": "require_escalated",
		"justification":       "Needs host access",
	}, Context{WorkspaceRoot: t.TempDir(), Interaction: interaction})
	if err != nil {
		t.Fatalf("Bash returned error: %v", err)
	}
	if result.IsError || !strings.Contains(result.Content, "stdout:\napproved") {
		t.Fatalf("unexpected approved result: %#v", result)
	}
}

type stubInteraction struct {
	decision ApprovalDecision
	answer   string
	approval ApprovalRequest
	question QuestionRequest
	err      error
}

func (s *stubInteraction) RequestApproval(_ context.Context, request ApprovalRequest) (ApprovalDecision, error) {
	s.approval = request
	if s.err != nil {
		return ApprovalDeny, s.err
	}
	return s.decision, nil
}

func (s *stubInteraction) AskUser(_ context.Context, request QuestionRequest) (string, error) {
	s.question = request
	if s.err != nil {
		return "", s.err
	}
	return s.answer, nil
}

var _ Interaction = (*stubInteraction)(nil)
