package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/zhenxin-dev/banka-code/internal/agent"
	"github.com/zhenxin-dev/banka-code/internal/config"
	"github.com/zhenxin-dev/banka-code/internal/instructions"
	"github.com/zhenxin-dev/banka-code/internal/llm"
	mcpclient "github.com/zhenxin-dev/banka-code/internal/mcp"
	"github.com/zhenxin-dev/banka-code/internal/messages"
	"github.com/zhenxin-dev/banka-code/internal/permissions"
	"github.com/zhenxin-dev/banka-code/internal/prompt"
	"github.com/zhenxin-dev/banka-code/internal/skills"
	"github.com/zhenxin-dev/banka-code/internal/tools"
	"github.com/zhenxin-dev/banka-code/internal/tui"
)

var version = "0.1.0"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
}

func run(args []string) error {
	promptArgs := make([]string, 0, len(args))
	var permissionOverride *permissions.Mode
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch arg {
		case "-v", "--version":
			fmt.Printf("Banka Code v%s\n", version)
			return nil
		case "-h", "--help":
			printHelp()
			return nil
		case "--permission-mode":
			if index+1 >= len(args) {
				return fmt.Errorf("--permission-mode 需要一个值：default、full-access 或 yolo")
			}
			index++
			mode, err := permissions.ParseMode(args[index])
			if err != nil {
				return err
			}
			permissionOverride = &mode
		default:
			if value, ok := strings.CutPrefix(arg, "--permission-mode="); ok {
				mode, err := permissions.ParseMode(value)
				if err != nil {
					return err
				}
				permissionOverride = &mode
				continue
			}
			promptArgs = append(promptArgs, arg)
		}
	}

	workspaceRoot, err := os.Getwd()
	if err != nil {
		return err
	}
	runtimeConfig, err := config.Load(workspaceRoot)
	if err != nil {
		return err
	}
	if permissionOverride != nil {
		runtimeConfig.PermissionMode = *permissionOverride
	}
	model, err := llm.NewClient(runtimeConfig)
	if err != nil {
		return err
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	instructionSet, err := instructions.Load(workspaceRoot, homeDir)
	if err != nil {
		return err
	}
	skillCatalog, err := skills.Discover(instructionSet.ProjectRoot, homeDir)
	if err != nil {
		return err
	}
	systemPrompt := prompt.Build(instructionSet, skillCatalog)
	definitions := tools.CreateBuiltins()
	if len(skillCatalog.Skills) > 0 {
		definitions = append(definitions, skills.NewTool(skillCatalog))
	}
	ctx := context.Background()
	mcpConfig, err := mcpclient.LoadConfig(instructionSet.ProjectRoot, homeDir)
	if err != nil {
		return err
	}
	mcpManager := mcpclient.NewManager(instructionSet.ProjectRoot, version)
	definitions = append(definitions, mcpManager.Connect(ctx, mcpConfig)...)
	defer mcpManager.Close()
	details := tui.RuntimeDetails{InstructionFiles: len(instructionSet.Documents), Skills: len(skillCatalog.Skills)}
	for _, status := range mcpManager.Statuses() {
		if status.Error != "" {
			fmt.Fprintf(os.Stderr, "MCP %s 连接失败: %s\n", status.Name, status.Error)
			details.MCP = append(details.MCP, fmt.Sprintf("%s 失败", status.Name))
		} else {
			details.MCP = append(details.MCP, fmt.Sprintf("%s 已连接（%d tools）", status.Name, status.ToolCount))
		}
	}
	registry := tools.NewRegistry(definitions)
	toolContext := tools.Context{
		WorkspaceRoot: runtimeConfig.WorkspaceRoot,
		Permissions:   permissions.NewPolicy(runtimeConfig.PermissionMode),
	}

	userPrompt := strings.TrimSpace(strings.Join(promptArgs, " "))
	if userPrompt == "" {
		return tui.Run(ctx, os.Stdin, os.Stdout, version, runtimeConfig, systemPrompt, details, model, registry, toolContext)
	}
	toolContext.Interaction = newConsoleInteraction(os.Stdin, os.Stderr)

	result, err := agent.Run(ctx, agent.RunOptions{
		SystemPrompt:      systemPrompt,
		InitialUserPrompt: userPrompt,
		Model:             model,
		ToolRegistry:      registry,
		ToolContext:       toolContext,
		OnToolCall: func(toolCall messages.ToolCall) {
			fmt.Fprintf(os.Stderr, "Tool: %s\n", toolCall.Name)
		},
	})
	if err != nil {
		return err
	}

	fmt.Println(result.FinalText)
	return nil
}

func printHelp() {
	fmt.Printf(`Banka Code v%s

用法:
  banka              启动交互模式
  banka <提示词>     单次执行模式

选项:
  -h, --help         显示帮助
  -v, --version      显示版本号
  --permission-mode  权限模式：default、full-access、yolo
`, version)
}
