package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/zhenxin-dev/banka-code/internal/llm"
	"github.com/zhenxin-dev/banka-code/internal/messages"
)

func TestCompactSummarizesOldTurnsAndKeepsRecentTurns(t *testing.T) {
	transcript := []messages.Message{
		messages.NewUserMessage("turn one"), messages.NewAssistantMessage("reply one", nil),
		messages.NewUserMessage("turn two"), messages.NewAssistantMessage("reply two", nil),
		messages.NewUserMessage("turn three"), messages.NewAssistantMessage("reply three", nil),
	}
	model := &scriptedClient{responses: []llm.GenerateResponse{{Text: "summary of turn one"}}}
	result, err := Compact(context.Background(), model, transcript, 2)
	if err != nil {
		t.Fatalf("Compact returned error: %v", err)
	}
	if result.CompactedMessages != 2 || len(result.Transcript) != 6 {
		t.Fatalf("unexpected compact result: %#v", result)
	}
	if result.Transcript[0].Role != "user" || result.Transcript[2].Content != "turn two" {
		t.Fatalf("recent turns were not preserved: %#v", result.Transcript)
	}
	request := model.requests[0]
	if len(request.Tools) != 0 || request.Messages[len(request.Messages)-2].Content != "reply one" {
		t.Fatalf("unexpected summarization request: %#v", request)
	}
}

func TestCompactRejectsShortTranscript(t *testing.T) {
	_, err := Compact(context.Background(), &scriptedClient{}, []messages.Message{
		messages.NewUserMessage("only"), messages.NewAssistantMessage("one", nil),
	}, 2)
	if !errors.Is(err, ErrNothingToCompact) {
		t.Fatalf("got error %v, want ErrNothingToCompact", err)
	}
}
