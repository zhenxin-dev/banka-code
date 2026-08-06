package agent

import (
	"context"
	"errors"
	"strings"

	"github.com/zhenxin-dev/banka-code/internal/llm"
	"github.com/zhenxin-dev/banka-code/internal/messages"
)

// ErrNothingToCompact indicates that the transcript has too few complete turns.
var ErrNothingToCompact = errors.New("没有足够的历史会话可压缩")

// CompactResult contains the replacement transcript and compaction statistics.
type CompactResult struct {
	Transcript        []messages.Message
	Summary           string
	CompactedMessages int
}

// Compact summarizes older complete turns while retaining recent turns verbatim.
func Compact(ctx context.Context, model llm.Client, transcript []messages.Message, keepRecentTurns int) (CompactResult, error) {
	cut := compactionCutIndex(transcript, keepRecentTurns)
	if cut <= 0 {
		return CompactResult{}, ErrNothingToCompact
	}
	response, err := model.Generate(ctx, llm.GenerateRequest{
		SystemPrompt: "Summarize the earlier coding-agent conversation in Chinese. Preserve user requirements, decisions, constraints, file paths, commands, tool findings, errors, and unfinished work. Be concise and factual. Do not call tools.",
		Messages: append(append([]messages.Message(nil), transcript[:cut]...),
			messages.NewUserMessage("请生成供后续 Agent 继续工作的上下文摘要。")),
	})
	if err != nil {
		return CompactResult{}, err
	}
	summary := strings.TrimSpace(response.Text)
	if summary == "" {
		return CompactResult{}, errors.New("模型返回了空的上下文摘要")
	}
	compacted := []messages.Message{
		messages.NewUserMessage("[Earlier conversation summary]\n" + summary),
		messages.NewAssistantMessage("已载入以上上下文摘要。", nil),
	}
	compacted = append(compacted, transcript[cut:]...)
	return CompactResult{Transcript: compacted, Summary: summary, CompactedMessages: cut}, nil
}

func compactionCutIndex(transcript []messages.Message, keepRecentTurns int) int {
	if keepRecentTurns < 1 {
		keepRecentTurns = 1
	}
	var userIndices []int
	for index, message := range transcript {
		if message.Role == "user" {
			userIndices = append(userIndices, index)
		}
	}
	if len(userIndices) <= keepRecentTurns {
		return 0
	}
	return userIndices[len(userIndices)-keepRecentTurns]
}
