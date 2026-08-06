package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/zhenxin-dev/banka-code/internal/tools"
)

type consoleInteraction struct {
	reader *bufio.Reader
	writer io.Writer
}

func newConsoleInteraction(input io.Reader, output io.Writer) *consoleInteraction {
	return &consoleInteraction{reader: bufio.NewReader(input), writer: output}
}

func (i *consoleInteraction) RequestApproval(ctx context.Context, request tools.ApprovalRequest) (tools.ApprovalDecision, error) {
	if err := ctx.Err(); err != nil {
		return tools.ApprovalDeny, err
	}
	fmt.Fprintf(i.writer, "\n需要执行受限操作：\n%s\n理由：%s\n\n1. 允许一次\n2. 始终允许此类操作（本次会话）\n3. 拒绝\n选择 [1-3]：", request.Command, request.Justification)
	answer, err := i.reader.ReadString('\n')
	if err != nil && len(answer) == 0 {
		return tools.ApprovalDeny, err
	}
	switch strings.TrimSpace(answer) {
	case "1":
		return tools.ApprovalAllowOnce, nil
	case "2":
		return tools.ApprovalAllowAlways, nil
	default:
		return tools.ApprovalDeny, nil
	}
}

func (i *consoleInteraction) AskUser(ctx context.Context, request tools.QuestionRequest) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	fmt.Fprintln(i.writer, "\n"+request.Question)
	for index, option := range request.Options {
		fmt.Fprintf(i.writer, "%d. %s\n", index+1, option)
	}
	_, _ = fmt.Fprint(i.writer, "回答：")
	answer, err := i.reader.ReadString('\n')
	if err != nil && len(answer) == 0 {
		return "", err
	}
	answer = strings.TrimSpace(answer)
	if index, parseErr := strconv.Atoi(answer); parseErr == nil && index >= 1 && index <= len(request.Options) {
		return request.Options[index-1], nil
	}
	return answer, nil
}

var _ tools.Interaction = (*consoleInteraction)(nil)
