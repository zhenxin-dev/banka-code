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
	lspclient "github.com/zhenxin-dev/banka-code/internal/lsp"
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
	profile := ""
	disableSkills := false
	disableMCP := false
	disableLSP := false
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch arg {
		case "-v", "--version":
			fmt.Printf("Banka Code v%s\n", version)
			return nil
		case "-h", "--help":
			printHelp()
			return nil
		case "--no-skills":
			disableSkills = true
		case "--no-mcp":
			disableMCP = true
		case "--no-lsp":
			disableLSP = true
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
		case "--profile":
			if index+1 >= len(args) {
				return fmt.Errorf("--profile 需要一个名称")
			}
			index++
			profile = strings.TrimSpace(args[index])
			if profile == "" {
				return fmt.Errorf("--profile 需要一个非空名称")
			}
		default:
			if value, ok := strings.CutPrefix(arg, "--permission-mode="); ok {
				mode, err := permissions.ParseMode(value)
				if err != nil {
					return err
				}
				permissionOverride = &mode
				continue
			}
			if value, ok := strings.CutPrefix(arg, "--profile="); ok {
				profile = strings.TrimSpace(value)
				if profile == "" {
					return fmt.Errorf("--profile 需要一个非空名称")
				}
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
	skillCatalog := skills.Catalog{}
	if !disableSkills {
		skillCatalog, err = skills.Discover(instructionSet.ProjectRoot, homeDir)
		if err != nil {
			return err
		}
		for _, warning := range skillCatalog.Warnings {
			fmt.Fprintf(os.Stderr, "Skill 警告: %s\n", warning)
		}
	}
	systemPrompt := prompt.Build(instructionSet, skillCatalog)
	definitions := tools.CreateBuiltins()
	var skillTool skills.Tool
	if len(skillCatalog.Skills) > 0 {
		skillTool = skills.NewTool(skillCatalog)
		definitions = append(definitions, skillTool)
	}
	ctx := context.Background()
	details := tui.RuntimeDetails{InstructionFiles: len(instructionSet.Documents), Skills: len(skillCatalog.Skills)}
	var lspManager *lspclient.Manager
	var mcpManager *mcpclient.Manager
	if !disableLSP {
		lspConfig, loadErr := lspclient.LoadConfig(instructionSet.ProjectRoot, homeDir)
		if loadErr != nil {
			return loadErr
		}
		if lspConfig.Enabled {
			lspManager = lspclient.NewManager(instructionSet.ProjectRoot, version, lspConfig)
			definitions = append(definitions, lspManager.NewTool())
			defer lspManager.Close()
			for _, status := range lspManager.Statuses() {
				if !status.Available {
					details.LSP = append(details.LSP, fmt.Sprintf("%s 不可用", status.Name))
				} else {
					details.LSP = append(details.LSP, fmt.Sprintf("%s 可用（懒启动）", status.Name))
				}
			}
		} else {
			details.LSP = append(details.LSP, "配置已禁用")
		}
	} else {
		details.LSP = append(details.LSP, "已禁用")
	}
	// MCP definitions are replaced dynamically after slow discovery,
	// tools/list_changed notifications, and interactive reloads. Preserve every
	// static tool registered so far, including LSP, when rebuilding the registry.
	baseDefinitions := append([]tools.Definition(nil), definitions...)
	if !disableMCP {
		mcpConfig, loadErr := mcpclient.LoadConfigWithProfile(instructionSet.ProjectRoot, homeDir, profile)
		if loadErr != nil {
			return loadErr
		}
		mcpManager = mcpclient.NewManager(instructionSet.ProjectRoot, version)
		definitions = append(definitions, mcpManager.Connect(ctx, mcpConfig)...)
		defer mcpManager.Close()
		for _, status := range mcpManager.Statuses() {
			if status.Error != "" {
				fmt.Fprintf(os.Stderr, "MCP %s 连接失败: %s\n", status.Name, status.Error)
				details.MCP = append(details.MCP, fmt.Sprintf("%s 失败", status.Name))
			} else {
				details.MCP = append(details.MCP, fmt.Sprintf("%s 已连接（%d tools）", status.Name, status.ToolCount))
			}
		}
	} else {
		details.MCP = append(details.MCP, "已禁用")
	}
	registry := tools.NewRegistry(definitions)
	if mcpManager != nil {
		mcpManager.SetToolsChangedHandler(func(remoteDefinitions []tools.Definition) {
			combined := append(append([]tools.Definition(nil), baseDefinitions...), remoteDefinitions...)
			registry.Replace(combined)
		})
	}
	toolContext := tools.Context{
		WorkspaceRoot: runtimeConfig.WorkspaceRoot,
		Permissions:   permissions.NewPolicy(runtimeConfig.PermissionMode),
	}
	if reader, ok := any(skillTool).(tools.InternalURIReader); ok {
		toolContext.URIReader = reader
	}
	if lspManager != nil {
		toolContext.FileObserver = lspManager
	}
	details.Actions.SkillNames = skillCatalog.Names()
	if !disableSkills {
		details.Actions.InvokeSkill = func(ctx context.Context, name string, _ string) (string, error) {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			_, content, loadErr := skillCatalog.Load(name)
			if loadErr == nil && skillTool != nil {
				loadErr = skillTool.MarkLoaded(name)
			}
			return content, loadErr
		}
	}
	if mcpManager != nil {
		details.Actions.MCP = func(ctx context.Context, command string) (string, error) {
			return runMCPCommand(ctx, mcpManager, instructionSet.ProjectRoot, homeDir, command, profile)
		}
	}
	if lspManager != nil {
		details.Actions.LSP = func(ctx context.Context, command string) (string, error) {
			return runLSPCommand(ctx, lspManager, command, instructionSet.ProjectRoot, homeDir)
		}
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

func runMCPCommand(ctx context.Context, manager *mcpclient.Manager, projectRoot string, homeDir string, command string, profiles ...string) (string, error) {
	command = strings.TrimSpace(command)
	fields := strings.Fields(command)
	action := "list"
	if len(fields) > 0 {
		action = strings.ToLower(fields[0])
	}
	switch action {
	case "", "list", "status":
		statuses := manager.Statuses()
		if len(statuses) == 0 {
			return "没有配置 MCP 服务器。", nil
		}
		lines := []string{"## MCP 服务器"}
		for _, status := range statuses {
			state := "未连接"
			if status.Connecting {
				state = "连接中"
			} else if status.Connected {
				state = fmt.Sprintf("已连接（%d tools）", status.ToolCount)
			}
			if status.Error != "" {
				state += "：" + status.Error
			}
			lines = append(lines, fmt.Sprintf("- %s [%s] %s", status.Name, status.Transport, state))
		}
		return strings.Join(lines, "\n"), nil
	case "tools":
		definitions := manager.ToolDefinitions()
		if len(definitions) == 0 {
			return "当前没有已发现的 MCP tools。", nil
		}
		lines := []string{"## MCP tools"}
		for _, definition := range definitions {
			lines = append(lines, "- `"+definition.Name()+"`："+definition.Description())
		}
		return strings.Join(lines, "\n"), nil
	case "reconnect":
		if len(fields) != 2 || strings.TrimSpace(fields[1]) == "" {
			return "用法：/mcp reconnect <server>", nil
		}
		if err := manager.Reconnect(ctx, fields[1]); err != nil {
			return "", err
		}
		return "已重新连接 MCP 服务器：" + fields[1], nil
	case "reload":
		profile := ""
		if len(profiles) > 0 {
			profile = profiles[0]
		}
		config, err := mcpclient.LoadConfigWithProfile(projectRoot, homeDir, profile)
		if err != nil {
			return "", err
		}
		manager.Connect(ctx, config)
		return "已重新加载 MCP 配置。", nil
	case "help":
		return "用法：/mcp [list|tools|reconnect <server>|reload|help]", nil
	default:
		return "未知 MCP 命令。用 /mcp help 查看用法。", nil
	}
}

func runLSPCommand(ctx context.Context, manager *lspclient.Manager, command string, roots ...string) (string, error) {
	command = strings.TrimSpace(command)
	fields := strings.Fields(command)
	action := "status"
	if len(fields) > 0 {
		action = strings.ToLower(fields[0])
	}
	switch action {
	case "", "list", "status":
		statuses := manager.Statuses()
		if len(statuses) == 0 {
			return "当前没有检测到语言服务器。", nil
		}
		lines := []string{"## LSP 服务器"}
		for _, status := range statuses {
			state := "不可用"
			if status.Connecting {
				state = "启动中"
			} else if status.Running {
				state = fmt.Sprintf("运行中（%d open，%d diagnostics）", status.OpenDocuments, status.Diagnostics)
			} else if status.Available {
				state = "可用（尚未启动）"
			}
			if status.Error != "" {
				state += "：" + status.Error
			}
			lines = append(lines, fmt.Sprintf("- %s [%s] %s", status.Name, status.Command, state))
		}
		return strings.Join(lines, "\n"), nil
	case "reload":
		if len(roots) >= 2 {
			config, loadErr := lspclient.LoadConfig(roots[0], roots[1])
			if loadErr != nil {
				return "", loadErr
			}
			if err := manager.ReloadConfiguration(ctx, config); err != nil {
				return "", err
			}
		}
		if len(fields) > 2 {
			return "用法：/lsp reload [server]", nil
		}
		if len(fields) == 2 {
			if err := manager.Reload(ctx, fields[1]); err != nil {
				return "", err
			}
			return "已重新加载 LSP：" + fields[1], nil
		}
		var failures []string
		for _, name := range manager.Config().Names() {
			if err := manager.Reload(ctx, name); err != nil {
				failures = append(failures, name+": "+err.Error())
			}
		}
		if len(failures) > 0 {
			return "LSP reload 部分失败：\n" + strings.Join(failures, "\n"), nil
		}
		return "已重新加载全部可用 LSP。", nil
	case "help":
		return "用法：/lsp [status|reload [server]|help]", nil
	default:
		return "未知 LSP 命令。用 /lsp help 查看用法。", nil
	}
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
  --profile         MCP 用户配置 profile（兼容 OMP_PROFILE/PI_PROFILE）
  --no-skills        禁用技能发现与 Skill 工具
  --no-mcp           禁用 MCP 服务器
  --no-lsp           禁用 LSP 工具与写入后诊断
`, version)
}
