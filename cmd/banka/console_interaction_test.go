package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/zhenxin-dev/banka-code/internal/tools"
)

func TestConsoleInteractionApprovalAndQuestion(t *testing.T) {
	var output bytes.Buffer
	interaction := newConsoleInteraction(strings.NewReader("y\n2\n"), &output)
	decision, err := interaction.RequestApproval(context.Background(), tools.ApprovalRequest{
		Command: "curl example.com", Justification: "需要联网",
	})
	if err != nil || decision != tools.ApprovalAllowOnce {
		t.Fatalf("unexpected approval: decision=%q err=%v", decision, err)
	}
	answer, err := interaction.AskUser(context.Background(), tools.QuestionRequest{
		Question: "选择方案", Options: []string{"A", "B"},
	})
	if err != nil || answer != "B" {
		t.Fatalf("unexpected answer: answer=%q err=%v", answer, err)
	}
	if !strings.Contains(output.String(), "需要联网") || !strings.Contains(output.String(), "2. B") {
		t.Fatalf("unexpected console output: %q", output.String())
	}
}
