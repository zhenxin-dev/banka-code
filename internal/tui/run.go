// Package tui contains the terminal interaction mode.
package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/glamour/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/zhenxin-dev/banka-code/internal/agent"
	"github.com/zhenxin-dev/banka-code/internal/config"
	"github.com/zhenxin-dev/banka-code/internal/llm"
	"github.com/zhenxin-dev/banka-code/internal/messages"
	"github.com/zhenxin-dev/banka-code/internal/permissions"
	"github.com/zhenxin-dev/banka-code/internal/prompt"
	"github.com/zhenxin-dev/banka-code/internal/tools"
)

var tuiColors = struct {
	text, brand, shimmer, inactive, subtle, success, err, warning string
	user, assistant, tool, border, status, hint, divider          string
}{
	text: "#fff6f0", brand: "#ff8fb4", shimmer: "#ffd2df", inactive: "#f4e6df",
	subtle: "#d8bbb0", success: "#7fb069", err: "#ff9aa6", warning: "#ffc27a",
	user: "#ffd4a0", assistant: "#fff7f2", tool: "#e8c4a4", border: "#b89a96",
	status: "#ffe5cb", hint: "#d4bdb6", divider: "#c9a09c",
}

type uiEntry struct {
	kind          string
	body          string
	detail        string
	toolID        string
	toolNameValue string
	toolState     string
}

// RuntimeDetails contains discovered capabilities shown by /status.
type RuntimeDetails struct {
	InstructionFiles int
	Skills           int
	MCP              []string
	LSP              []string
	// Actions exposes optional runtime management hooks to the interactive
	// command layer. Keeping these callbacks at the boundary avoids coupling
	// the TUI package to the MCP/LSP/skills implementations and also lets
	// embedders provide their own managers.
	Actions RuntimeActions
}

// RuntimeActions are optional handlers for interactive capability commands.
// A nil handler leaves the corresponding command unavailable but does not
// affect model-facing tools.
type RuntimeActions struct {
	SkillNames  []string
	InvokeSkill func(context.Context, string, string) (string, error)
	MCP         func(context.Context, string) (string, error)
	LSP         func(context.Context, string) (string, error)
}

type animationTickMsg time.Time
type agentTextDeltaMsg string
type agentToolCallMsg messages.ToolCall
type agentToolResultMsg messages.Message
type agentFinishedMsg struct {
	result agent.RunResult
	err    error
}
type compactFinishedMsg struct {
	result agent.CompactResult
	err    error
}

type interactionResponse struct {
	decision tools.ApprovalDecision
	answer   string
}

type approvalRequestMsg struct {
	request  tools.ApprovalRequest
	response chan interactionResponse
}

type questionRequestMsg struct {
	request  tools.QuestionRequest
	response chan interactionResponse
}

type pendingInteraction struct {
	approval    *tools.ApprovalRequest
	question    *tools.QuestionRequest
	response    chan interactionResponse
	selection   int
	permissions bool
}

type approvalOption struct {
	label    string
	decision tools.ApprovalDecision
	status   string
}

type permissionModeOption struct {
	label string
	mode  permissions.Mode
}

func approvalOptions() []approvalOption {
	return []approvalOption{
		{label: "允许一次", decision: tools.ApprovalAllowOnce, status: "已允许本次受限操作"},
		{label: "始终允许此类操作（本次会话）", decision: tools.ApprovalAllowAlways, status: "本次会话将始终允许此类操作"},
		{label: "拒绝", decision: tools.ApprovalDeny, status: "已拒绝提权请求"},
	}
}

func permissionModeOptions() []permissionModeOption {
	return []permissionModeOption{
		{label: permissions.ModeDefault.Label(), mode: permissions.ModeDefault},
		{label: permissions.ModeFullAccess.Label(), mode: permissions.ModeFullAccess},
		{label: permissions.ModeYOLO.Label(), mode: permissions.ModeYOLO},
	}
}

type turnCheckpoint struct {
	entries    []uiEntry
	transcript []messages.Message
}

type tuiInteraction struct {
	events chan<- tea.Msg
}

func (i tuiInteraction) RequestApproval(ctx context.Context, request tools.ApprovalRequest) (tools.ApprovalDecision, error) {
	response := make(chan interactionResponse, 1)
	select {
	case i.events <- approvalRequestMsg{request: request, response: response}:
	case <-ctx.Done():
		return tools.ApprovalDeny, ctx.Err()
	}
	select {
	case result := <-response:
		return result.decision, nil
	case <-ctx.Done():
		return tools.ApprovalDeny, ctx.Err()
	}
}

func (i tuiInteraction) AskUser(ctx context.Context, request tools.QuestionRequest) (string, error) {
	response := make(chan interactionResponse, 1)
	select {
	case i.events <- questionRequestMsg{request: request, response: response}:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	select {
	case result := <-response:
		return result.answer, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

type screenPosition struct {
	x int
	y int
}

type appModel struct {
	ctx          context.Context
	runtime      config.RuntimeConfig
	client       llm.Client
	registry     *tools.Registry
	toolContext  tools.Context
	systemPrompt string
	details      RuntimeDetails
	version      string

	input    textinput.Model
	viewport viewport.Model
	width    int
	height   int

	entries          []uiEntry
	transcript       []messages.Message
	busy             bool
	cancel           context.CancelFunc
	agentEvents      chan tea.Msg
	streamingEntry   int
	commandSelection int
	animationTick    int
	hitokoto         string
	selectionStart   *screenPosition
	selectionEnd     *screenPosition
	selectionMoved   bool
	pending          *pendingInteraction
	checkpoints      []turnCheckpoint
	expandToolOutput bool
	history          []string
	historyIndex     int
	gitBranch        string
	gitDirty         bool
}

// Run starts the full-screen terminal mode.
func Run(ctx context.Context, input io.Reader, output io.Writer, version string, runtimeConfig config.RuntimeConfig, systemPrompt string, details RuntimeDetails, client llm.Client, registry *tools.Registry, toolContext tools.Context) error {
	model := newAppModel(ctx, version, runtimeConfig, client, registry, toolContext)
	model.systemPrompt = systemPrompt
	model.details = details
	program := tea.NewProgram(model,
		tea.WithContext(ctx),
		tea.WithInput(input),
		tea.WithOutput(output),
		tea.WithFPS(30),
	)
	_, err := program.Run()
	if errors.Is(err, context.Canceled) || errors.Is(err, tea.ErrInterrupted) {
		return nil
	}
	return err
}

func newAppModel(ctx context.Context, version string, runtimeConfig config.RuntimeConfig, client llm.Client, registry *tools.Registry, toolContext tools.Context) *appModel {
	input := textinput.New()
	input.Prompt = ""
	input.Placeholder = "给 Banka Code 发消息…"
	input.CharLimit = 0
	input.SetVirtualCursor(true)
	input.SetStyles(textinput.DefaultDarkStyles())
	view := viewport.New(viewport.WithWidth(80), viewport.WithHeight(16))
	view.MouseWheelEnabled = true
	view.SoftWrap = true
	view.FillHeight = true
	events := make(chan tea.Msg, 256)
	toolContext.Interaction = tuiInteraction{events: events}
	if toolContext.Permissions == nil {
		toolContext.Permissions = permissions.NewPolicy(runtimeConfig.PermissionMode)
	}
	gitBranch, gitDirty := detectGitState(runtimeConfig.WorkspaceRoot)
	return &appModel{
		ctx: ctx, runtime: runtimeConfig, client: client, registry: registry, toolContext: toolContext, version: version,
		systemPrompt: prompt.DefaultSystemPrompt,
		input:        input, viewport: view, width: 80, height: 24, streamingEntry: -1,
		agentEvents: events, historyIndex: -1,
		gitBranch: gitBranch,
		gitDirty:  gitDirty,
	}
}

func (m *appModel) Init() tea.Cmd {
	return tea.Batch(m.input.Focus(), animationTick(), fetchHitokoto)
}

func (m *appModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch message := message.(type) {
	case tea.WindowSizeMsg:
		// Keep the reported terminal size intact.  Rendering helpers apply their
		// own safe minimums, while the final view is clipped to these dimensions;
		// this matters when Banka is opened in a narrow split pane or over SSH.
		m.width = max(1, message.Width)
		m.height = max(1, message.Height)
		m.resize()
		m.refreshViewport()
		return m, nil
	case animationTickMsg:
		m.animationTick++
		return m, animationTick()
	case hitokotoMsg:
		m.hitokoto = string(message)
		m.resize()
		return m, nil
	case agentTextDeltaMsg:
		if m.streamingEntry >= 0 && m.streamingEntry < len(m.entries) {
			m.entries[m.streamingEntry].body += string(message)
			m.refreshViewport()
		}
		return m, waitForAgentEvent(m.agentEvents)
	case agentToolCallMsg:
		m.addToolCall(messages.ToolCall(message))
		m.refreshViewport()
		return m, waitForAgentEvent(m.agentEvents)
	case agentToolResultMsg:
		result := messages.Message(message)
		m.markToolResult(result)
		if result.IsError {
			body := result.Content
			if strings.TrimSpace(result.ToolName) != "" {
				body = result.ToolName + ": " + body
			}
			m.entries = append(m.entries, uiEntry{kind: "error", body: body})
		}
		m.refreshViewport()
		return m, waitForAgentEvent(m.agentEvents)
	case approvalRequestMsg:
		m.beginApproval(message)
		return m, m.input.Focus()
	case questionRequestMsg:
		m.beginQuestion(message)
		return m, m.input.Focus()
	case agentFinishedMsg:
		m.finishTurn(message)
		return m, m.input.Focus()
	case compactFinishedMsg:
		m.finishCompaction(message)
		return m, m.input.Focus()
	case tea.MouseClickMsg:
		mouse := message.Mouse()
		if mouse.Button == tea.MouseLeft {
			if position, ok := m.viewportPosition(mouse.X, mouse.Y); ok {
				m.selectionStart = &position
				m.selectionEnd = &position
				m.selectionMoved = false
			}
		}
		return m, nil
	case tea.MouseMotionMsg:
		if m.selectionStart != nil {
			if position, ok := m.viewportPosition(message.Mouse().X, message.Mouse().Y); ok {
				m.selectionEnd = &position
				m.selectionMoved = position != *m.selectionStart
			}
		}
		return m, nil
	case tea.MouseReleaseMsg:
		if m.selectionStart == nil {
			return m, nil
		}
		if position, ok := m.viewportPosition(message.Mouse().X, message.Mouse().Y); ok {
			m.selectionEnd = &position
			m.selectionMoved = m.selectionMoved || position != *m.selectionStart
		}
		selected := ""
		if m.selectionMoved && m.selectionEnd != nil {
			selected = extractVisibleSelection(m.viewport.View(), *m.selectionStart, *m.selectionEnd)
		}
		m.selectionStart = nil
		m.selectionEnd = nil
		m.selectionMoved = false
		if selected != "" {
			return m, tea.SetClipboard(selected)
		}
		return m, nil
	case tea.MouseWheelMsg:
		var command tea.Cmd
		m.viewport, command = m.viewport.Update(message)
		return m, command
	case tea.PasteMsg:
		if !m.busy {
			var command tea.Cmd
			m.input, command = m.input.Update(message)
			m.commandSelection = 0
			m.historyIndex = -1
			m.resize()
			return m, command
		}
		return m, nil
	case tea.KeyPressMsg:
		return m.handleKey(message)
	}
	return m, nil
}

func (m *appModel) handleKey(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	keyName := key.String()
	if keyName == "ctrl+c" {
		if m.cancel != nil {
			m.cancel()
		}
		return m, tea.Quit
	}
	if keyName == "ctrl+o" {
		// Match OMP's transcript behavior: keep the compact tool summary in
		// place and toggle only its detailed output. This remains useful while
		// an approval/question panel has focus as well.
		m.expandToolOutput = !m.expandToolOutput
		m.refreshViewport()
		return m, nil
	}
	if m.pending != nil {
		return m.handleInteractionKey(key)
	}
	if m.busy {
		if keyName == "esc" && m.cancel != nil {
			m.cancel()
		}
		return m, nil
	}
	// Once history browsing has started, keep the arrow keys in the history
	// ring even if the restored value happens to begin with '/'. Typing any
	// other character below exits history mode and returns arrows to the
	// command panel/text cursor.
	if (keyName == "up" || keyName == "down") && m.historyIndex >= 0 && m.historyIndex < len(m.history) && m.input.Value() == m.history[m.historyIndex] {
		direction := -1
		if keyName == "down" {
			direction = 1
		}
		if m.restoreHistory(direction) {
			m.resize()
			return m, nil
		}
	}

	suggestions := m.visibleCommandSuggestions()
	if len(suggestions) > 0 {
		switch keyName {
		case "up":
			m.commandSelection = moveCommandSelection(m.commandSelection, len(suggestions), -1)
			return m, nil
		case "down":
			m.commandSelection = moveCommandSelection(m.commandSelection, len(suggestions), 1)
			return m, nil
		case "tab":
			m.commandSelection = normalizeCommandSelection(m.commandSelection, len(suggestions))
			m.input.SetValue(suggestions[m.commandSelection].Command)
			m.input.CursorEnd()
			m.historyIndex = -1
			m.commandSelection = 0
			m.resize()
			return m, nil
		case "enter":
			m.commandSelection = normalizeCommandSelection(m.commandSelection, len(suggestions))
			return m.submit(suggestions[m.commandSelection].Command)
		}
	}

	switch keyName {
	case "enter":
		return m.submit(m.input.Value())
	case "pgup":
		m.viewport.PageUp()
		return m, nil
	case "pgdown":
		m.viewport.PageDown()
		return m, nil
	case "up":
		if m.restoreHistory(-1) {
			m.resize()
			return m, nil
		}
	case "down":
		if m.restoreHistory(1) {
			m.resize()
			return m, nil
		}
	}

	var command tea.Cmd
	m.input, command = m.input.Update(key)
	m.historyIndex = -1
	m.commandSelection = 0
	m.resize()
	return m, command
}

func (m *appModel) handleInteractionKey(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.pending == nil {
		return m, nil
	}
	if m.pending.approval != nil || m.pending.permissions || (m.pending.question != nil && len(m.pending.question.Options) > 0) {
		count := len(approvalOptions())
		if m.pending.permissions {
			count = len(permissionModeOptions())
		} else if m.pending.question != nil {
			count = len(m.pending.question.Options)
		}
		switch key.String() {
		case "up":
			m.pending.selection = moveCommandSelection(m.pending.selection, count, -1)
			m.resize()
			return m, nil
		case "down":
			m.pending.selection = moveCommandSelection(m.pending.selection, count, 1)
			m.resize()
			return m, nil
		case "enter":
			if m.pending.permissions {
				return m.selectPermissionMode()
			}
			if m.pending.question != nil {
				if strings.TrimSpace(m.input.Value()) != "" {
					return m.submitInteraction(m.input.Value())
				}
				return m.selectQuestionOption()
			}
			return m.selectApproval()
		case "esc":
			if m.pending.permissions {
				m.pending = nil
				m.input.Placeholder = "给 Banka Code 发消息…"
				m.resize()
				return m, m.input.Focus()
			}
			if m.pending.question != nil {
				return m.cancelPendingQuestion()
			}
			return m.resolveInteraction(interactionResponse{decision: tools.ApprovalDeny}, "已拒绝提权请求")
		}
		return m, nil
	}
	switch key.String() {
	case "esc":
		return m.cancelPendingQuestion()
	case "enter":
		return m.submitInteraction(m.input.Value())
	}
	var command tea.Cmd
	m.input, command = m.input.Update(key)
	m.resize()
	return m, command
}

func (m *appModel) cancelPendingQuestion() (tea.Model, tea.Cmd) {
	m.pending = nil
	m.input.Reset()
	m.input.Placeholder = "等待中…"
	if m.cancel != nil {
		m.cancel()
	}
	m.resize()
	m.refreshViewport()
	return m, waitForAgentEvent(m.agentEvents)
}

func (m *appModel) beginApproval(message approvalRequestMsg) {
	request := message.request
	m.pending = &pendingInteraction{approval: &request, response: message.response, selection: 0}
	m.input.Reset()
	m.input.Placeholder = "使用方向键选择…"
	m.entries = append(m.entries, uiEntry{kind: "approval", body: fmt.Sprintf(
		"请求执行受限操作\n\n操作：`%s`\n\n理由：%s", request.Command, request.Justification,
	)})
	m.resize()
	m.refreshViewport()
}

func (m *appModel) beginPermissionModeSelection() {
	selection := 0
	current := m.toolContext.PermissionMode()
	for index, option := range permissionModeOptions() {
		if option.mode == current {
			selection = index
			break
		}
	}
	m.pending = &pendingInteraction{permissions: true, selection: selection}
	m.input.Reset()
	m.input.Placeholder = "使用方向键选择…"
	m.resize()
}

func (m *appModel) beginQuestion(message questionRequestMsg) {
	request := message.request
	m.pending = &pendingInteraction{question: &request, response: message.response, selection: 0}
	m.input.Reset()
	m.input.Placeholder = "输入回答…"
	body := request.Question
	for index, option := range request.Options {
		body += fmt.Sprintf("\n\n%d. %s", index+1, option)
	}
	m.entries = append(m.entries, uiEntry{kind: "question", body: body})
	m.resize()
	m.refreshViewport()
}

func (m *appModel) submitInteraction(value string) (tea.Model, tea.Cmd) {
	if m.pending == nil {
		return m, nil
	}
	answer := strings.TrimSpace(value)
	if answer == "" {
		if m.pending != nil && m.pending.question != nil && len(m.pending.question.Options) > 0 {
			return m.selectQuestionOption()
		}
		return m, nil
	}
	if m.pending != nil && m.pending.question != nil {
		if index, err := strconv.Atoi(answer); err == nil && index >= 1 && index <= len(m.pending.question.Options) {
			answer = m.pending.question.Options[index-1]
		}
	}
	m.entries = append(m.entries, uiEntry{kind: "user", body: answer})
	return m.resolveInteraction(interactionResponse{answer: answer}, "")
}

func (m *appModel) selectApproval() (tea.Model, tea.Cmd) {
	if m.pending == nil || m.pending.approval == nil {
		return m, nil
	}
	options := approvalOptions()
	selection := normalizeCommandSelection(m.pending.selection, len(options))
	option := options[selection]
	return m.resolveInteraction(interactionResponse{decision: option.decision}, option.status)
}

func (m *appModel) selectQuestionOption() (tea.Model, tea.Cmd) {
	if m.pending == nil || m.pending.question == nil || len(m.pending.question.Options) == 0 {
		return m, nil
	}
	options := m.pending.question.Options
	selection := normalizeCommandSelection(m.pending.selection, len(options))
	answer := options[selection]
	m.entries = append(m.entries, uiEntry{kind: "user", body: answer})
	return m.resolveInteraction(interactionResponse{answer: answer}, "")
}

func (m *appModel) selectPermissionMode() (tea.Model, tea.Cmd) {
	if m.pending == nil || !m.pending.permissions {
		return m, nil
	}
	options := permissionModeOptions()
	selection := normalizeCommandSelection(m.pending.selection, len(options))
	mode := options[selection].mode
	if m.toolContext.Permissions == nil {
		m.toolContext.Permissions = permissions.NewPolicy(mode)
	} else {
		m.toolContext.Permissions.SetMode(mode)
	}
	m.runtime.PermissionMode = mode
	m.pending = nil
	m.input.Reset()
	m.input.Placeholder = "给 Banka Code 发消息…"
	m.entries = append(m.entries, uiEntry{kind: "status", body: "权限模式已切换为" + mode.Label()})
	m.resize()
	m.refreshViewport()
	return m, m.input.Focus()
}

func (m *appModel) resolveInteraction(response interactionResponse, status string) (tea.Model, tea.Cmd) {
	pending := m.pending
	if pending == nil {
		return m, nil
	}
	m.pending = nil
	m.input.Reset()
	m.input.Placeholder = "等待中…"
	if status != "" {
		m.entries = append(m.entries, uiEntry{kind: "status", body: status})
	}
	m.resize()
	m.refreshViewport()
	if pending.response != nil {
		pending.response <- response
	}
	return m, waitForAgentEvent(m.agentEvents)
}

func (m *appModel) submit(value string) (tea.Model, tea.Cmd) {
	userPrompt := strings.TrimSpace(value)
	if userPrompt == "" {
		return m, nil
	}
	m.recordHistory(userPrompt)
	m.input.Reset()
	m.commandSelection = 0
	m.resize()

	name, args, skillInvocation := parseSkillInvocation(userPrompt)
	if !skillInvocation {
		name, args, skillInvocation = parseEmbeddedSkillInvocation(userPrompt)
	}
	if skillInvocation {
		if m.details.Actions.InvokeSkill == nil {
			m.entries = append(m.entries, uiEntry{kind: "error", body: "Skill 命令不可用：当前未加载技能"})
			m.refreshViewport()
			return m, nil
		}
		content, err := m.details.Actions.InvokeSkill(m.ctx, name, args)
		if err != nil {
			m.entries = append(m.entries, uiEntry{kind: "error", body: err.Error()})
			m.refreshViewport()
			return m, nil
		}
		prompt := fmt.Sprintf("[Skill: %s]\n\n%s", name, content)
		if args != "" {
			prompt += "\n\n[User arguments]\n" + args
		}
		return m.startAgentTurn(userPrompt, prompt)
	}

	if command, ok := parseBuiltinCommand(userPrompt); ok {
		switch command.Name {
		case "exit", "quit":
			return m, tea.Quit
		case "clear":
			m.entries = nil
			m.transcript = nil
			m.checkpoints = nil
			m.streamingEntry = -1
			m.expandToolOutput = false
			m.refreshViewport()
			return m, nil
		case "undo":
			m.undoLastTurn()
			return m, nil
		case "compact":
			return m.startCompaction()
		case "permissions":
			m.beginPermissionModeSelection()
			return m, nil
		case "help":
			m.entries = append(m.entries, uiEntry{kind: "assistant", body: buildBuiltinHelpBody()})
			m.refreshViewport()
			return m, nil
		case "skills":
			m.entries = append(m.entries, uiEntry{kind: "assistant", body: m.skillsBody()})
			m.refreshViewport()
			return m, nil
		case "status":
			m.refreshGitState()
			m.resize()
			toolCount := 0
			if m.registry != nil {
				toolCount = len(m.registry.List())
			}
			body := fmt.Sprintf("## 当前状态\n\n- Provider：%s\n- Model：%s\n- 权限模式：%s\n- 已注册工具：%d\n- AGENTS 指令文件：%d\n- Skills：%d\n- 当前屏幕消息数：%d\n- 当前会话消息数：%d\n- 可回滚轮数：%d",
				providerLabel(m.runtime.Provider), m.runtime.Model, m.toolContext.PermissionMode().Label(), toolCount, m.details.InstructionFiles,
				m.details.Skills, len(m.entries), len(m.transcript), len(m.checkpoints))
			if len(m.details.MCP) == 0 {
				body += "\n- MCP：未配置"
			} else {
				body += "\n- MCP：" + strings.Join(m.details.MCP, "；")
			}
			if len(m.details.LSP) == 0 {
				body += "\n- LSP：未检测到可用服务器"
			} else {
				body += "\n- LSP：" + strings.Join(m.details.LSP, "；")
			}
			m.entries = append(m.entries, uiEntry{kind: "assistant", body: body})
			m.refreshViewport()
			return m, nil
		}
	}
	for _, managed := range []struct {
		name    string
		handler func(context.Context, string) (string, error)
	}{
		{name: "mcp", handler: m.details.Actions.MCP},
		{name: "lsp", handler: m.details.Actions.LSP},
	} {
		if commandArgs, ok := parseManagedCommand(userPrompt, managed.name); ok {
			if managed.handler == nil {
				m.entries = append(m.entries, uiEntry{kind: "error", body: "/" + managed.name + " 命令不可用"})
				m.refreshViewport()
				return m, nil
			}
			body, err := managed.handler(m.ctx, commandArgs)
			if err != nil {
				m.entries = append(m.entries, uiEntry{kind: "error", body: err.Error()})
			} else {
				m.entries = append(m.entries, uiEntry{kind: "assistant", body: body})
			}
			m.refreshViewport()
			return m, nil
		}
	}

	return m.startAgentTurn(userPrompt, userPrompt)
}

func (m *appModel) skillsBody() string {
	if len(m.details.Actions.SkillNames) == 0 {
		return "当前没有发现可用技能。"
	}
	lines := []string{"## 可用技能", ""}
	for _, name := range m.details.Actions.SkillNames {
		lines = append(lines, "- `"+name+"`：输入 `/skill:"+name+"` 调用")
	}
	return strings.Join(lines, "\n")
}

func (m *appModel) startAgentTurn(displayPrompt string, modelPrompt string) (tea.Model, tea.Cmd) {
	m.saveCheckpoint()
	m.busy = true
	m.input.Placeholder = "等待中…"
	m.entries = append(m.entries, uiEntry{kind: "user", body: displayPrompt})
	m.entries = append(m.entries, uiEntry{kind: "assistant"})
	m.streamingEntry = len(m.entries) - 1
	m.resize()
	m.refreshViewport()

	runContext, cancel := context.WithCancel(m.ctx)
	m.cancel = cancel
	previous := append([]messages.Message(nil), m.transcript...)
	return m, tea.Batch(
		runAgent(runContext, m.agentEvents, m.systemPrompt, m.client, m.registry, m.toolContext, previous, modelPrompt),
		waitForAgentEvent(m.agentEvents),
	)
}

func (m *appModel) startCompaction() (tea.Model, tea.Cmd) {
	m.saveCheckpoint()
	m.busy = true
	m.input.Placeholder = "正在压缩上下文…"
	m.entries = append(m.entries, uiEntry{kind: "status", body: "正在压缩较早的会话上下文…"})
	m.resize()
	m.refreshViewport()
	runContext, cancel := context.WithCancel(m.ctx)
	m.cancel = cancel
	transcript := append([]messages.Message(nil), m.transcript...)
	return m, func() tea.Msg {
		result, err := agent.Compact(runContext, m.client, transcript, 2)
		return compactFinishedMsg{result: result, err: err}
	}
}

func (m *appModel) finishCompaction(message compactFinishedMsg) {
	if m.cancel != nil {
		m.cancel()
	}
	m.cancel = nil
	m.busy = false
	m.input.Placeholder = "给 Banka Code 发消息…"
	if message.err != nil {
		if len(m.checkpoints) > 0 {
			m.checkpoints = m.checkpoints[:len(m.checkpoints)-1]
		}
		if !errors.Is(message.err, context.Canceled) {
			m.entries = append(m.entries, uiEntry{kind: "error", body: message.err.Error()})
		}
		m.resize()
		m.refreshViewport()
		return
	}
	m.transcript = message.result.Transcript
	m.entries = append(m.entries, uiEntry{kind: "status", body: fmt.Sprintf(
		"已将 %d 条较早消息压缩为摘要，最近两轮保留原文", message.result.CompactedMessages,
	)})
	m.resize()
	m.refreshViewport()
}

func (m *appModel) saveCheckpoint() {
	m.checkpoints = append(m.checkpoints, turnCheckpoint{
		entries:    append([]uiEntry(nil), m.entries...),
		transcript: append([]messages.Message(nil), m.transcript...),
	})
}

func (m *appModel) undoLastTurn() {
	if len(m.checkpoints) == 0 {
		m.entries = append(m.entries, uiEntry{kind: "status", body: "没有可回滚的会话"})
		m.refreshViewport()
		return
	}
	checkpoint := m.checkpoints[len(m.checkpoints)-1]
	m.checkpoints = m.checkpoints[:len(m.checkpoints)-1]
	m.entries = append([]uiEntry(nil), checkpoint.entries...)
	m.transcript = append([]messages.Message(nil), checkpoint.transcript...)
	m.streamingEntry = -1
	m.entries = append(m.entries, uiEntry{kind: "status", body: "已回滚上一轮会话上下文；工作区文件未更改"})
	m.refreshViewport()
}

func runAgent(ctx context.Context, events chan<- tea.Msg, systemPrompt string, client llm.Client, registry *tools.Registry, toolContext tools.Context, previous []messages.Message, userPrompt string) tea.Cmd {
	return func() tea.Msg {
		result, err := agent.Run(ctx, agent.RunOptions{
			SystemPrompt:      systemPrompt,
			InitialUserPrompt: userPrompt,
			PreviousMessages:  previous,
			Model:             client,
			ToolRegistry:      registry,
			ToolContext:       toolContext,
			OnTextDelta: func(delta string) {
				events <- agentTextDeltaMsg(delta)
			},
			OnToolCall: func(call messages.ToolCall) {
				events <- agentToolCallMsg(call)
			},
			OnToolResult: func(result messages.Message) {
				events <- agentToolResultMsg(result)
			},
		})
		events <- agentFinishedMsg{result: result, err: err}
		return nil
	}
}

func waitForAgentEvent(events <-chan tea.Msg) tea.Cmd {
	return func() tea.Msg { return <-events }
}

func (m *appModel) addToolCall(call messages.ToolCall) {
	toolEntry := uiEntry{kind: "tool", body: formatToolCall(call), detail: call.ArgumentsJSON, toolID: call.ID, toolNameValue: call.Name, toolState: "running"}
	if m.streamingEntry >= 0 && m.streamingEntry < len(m.entries) && m.entries[m.streamingEntry].body == "" {
		m.entries[m.streamingEntry] = toolEntry
	} else {
		m.entries = append(m.entries, toolEntry)
	}
	m.entries = append(m.entries, uiEntry{kind: "assistant"})
	m.streamingEntry = len(m.entries) - 1
}

func (m *appModel) markToolResult(result messages.Message) {
	for index := len(m.entries) - 1; index >= 0; index-- {
		entry := &m.entries[index]
		if entry.kind != "tool" {
			continue
		}
		if result.ToolCallID != "" {
			if entry.toolID != "" && entry.toolID != result.ToolCallID {
				continue
			}
			// Older providers may omit the call ID on the UI entry. If a tool
			// name is available, use it as a safe fallback rather than marking
			// an unrelated call.
			if entry.toolID == "" && result.ToolName != "" && entry.toolName() != result.ToolName {
				continue
			}
		} else if result.ToolName != "" && entry.toolName() != result.ToolName {
			continue
		}
		if entry.toolState != "" && entry.toolState != "running" {
			continue
		}
		if result.IsError {
			entry.toolState = "error"
		} else {
			entry.toolState = "success"
		}
		entry.detail = result.Content
		return
	}
}

func (entry uiEntry) toolName() string {
	if strings.TrimSpace(entry.toolNameValue) != "" {
		return strings.TrimSpace(entry.toolNameValue)
	}
	return strings.TrimSpace(strings.SplitN(entry.body, " · ", 2)[0])
}

func (m *appModel) finishTurn(message agentFinishedMsg) {
	if m.cancel != nil {
		m.cancel()
	}
	m.cancel = nil
	m.pending = nil
	m.busy = false
	m.input.Placeholder = "给 Banka Code 发消息…"

	if message.err == nil {
		if m.streamingEntry >= 0 && m.streamingEntry < len(m.entries) && m.entries[m.streamingEntry].body == "" && message.result.FinalText != "" {
			m.entries[m.streamingEntry].body = message.result.FinalText
		}
		m.transcript = message.result.Transcript
		m.removeEmptyStreamingEntry()
		m.entries = append(m.entries, uiEntry{kind: "status", body: fmt.Sprintf("✓ %d 轮", message.result.Iterations)})
	} else {
		m.removeEmptyStreamingEntry()
		if !errors.Is(message.err, context.Canceled) {
			m.entries = append(m.entries, uiEntry{kind: "error", body: message.err.Error()})
		}
	}
	m.streamingEntry = -1
	m.refreshGitState()
	m.resize()
	m.refreshViewport()
}

func (m *appModel) refreshGitState() {
	branch, dirty := detectGitState(m.runtime.WorkspaceRoot)
	if branch == "" {
		return
	}
	m.gitBranch = branch
	m.gitDirty = dirty
}

func (m *appModel) removeEmptyStreamingEntry() {
	if m.streamingEntry >= 0 && m.streamingEntry < len(m.entries) && m.entries[m.streamingEntry].body == "" {
		m.entries = append(m.entries[:m.streamingEntry], m.entries[m.streamingEntry+1:]...)
	}
}

func (m *appModel) visibleCommandSuggestions() []builtinCommand {
	commands := findCommands(m.input.Value(), m.details.Actions.SkillNames)
	if len(commands) > 6 {
		return commands[:6]
	}
	return commands
}

func (m *appModel) recordHistory(value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	if len(m.history) == 0 || m.history[len(m.history)-1] != value {
		m.history = append(m.history, value)
		if len(m.history) > 100 {
			m.history = m.history[len(m.history)-100:]
		}
	}
	m.historyIndex = -1
}

// restoreHistory moves through the local prompt history. It only takes over
// the arrow keys when the draft is empty or already being browsed, preserving
// the text input's normal cursor behavior for an in-progress draft.
func (m *appModel) restoreHistory(direction int) bool {
	if direction != -1 && direction != 1 {
		return false
	}
	if len(m.history) == 0 || (m.historyIndex < 0 && strings.TrimSpace(m.input.Value()) != "") {
		return false
	}
	if direction < 0 {
		if m.historyIndex < 0 {
			m.historyIndex = len(m.history) - 1
		} else if m.historyIndex > 0 {
			m.historyIndex--
		}
	} else {
		if m.historyIndex < 0 {
			return false
		}
		if m.historyIndex >= len(m.history)-1 {
			m.historyIndex = -1
			m.input.Reset()
			return true
		}
		m.historyIndex++
	}
	if m.historyIndex >= 0 && m.historyIndex < len(m.history) {
		m.input.SetValue(m.history[m.historyIndex])
		m.input.CursorEnd()
	}
	return true
}

func (m *appModel) viewportPosition(x int, y int) (screenPosition, bool) {
	if m.welcomeVisible() {
		return screenPosition{}, false
	}
	viewportLeft := 0
	if m.width >= 4 {
		viewportLeft = 1
	}
	position := screenPosition{x: x - viewportLeft, y: y}
	return position, position.x >= 0 && position.x < m.viewport.Width() && position.y >= 0 && position.y < m.viewport.Height()
}

func extractVisibleSelection(view string, start screenPosition, end screenPosition) string {
	if end.y < start.y || (end.y == start.y && end.x < start.x) {
		start, end = end, start
	}
	lines := strings.Split(view, "\n")
	if start.y < 0 || start.y >= len(lines) || end.y < 0 || end.y >= len(lines) {
		return ""
	}
	var selected []string
	for lineIndex := start.y; lineIndex <= end.y; lineIndex++ {
		line := lines[lineIndex]
		left := 0
		right := ansi.StringWidth(line)
		if lineIndex == start.y {
			left = min(max(0, start.x), right)
		}
		if lineIndex == end.y {
			right = min(max(0, end.x+1), right)
		}
		if right < left {
			right = left
		}
		selected = append(selected, strings.TrimRight(ansi.Strip(ansi.Cut(line, left, right)), " "))
	}
	return strings.TrimSpace(strings.Join(selected, "\n"))
}

func (m *appModel) resize() {
	contentWidth := m.contentWidth()
	m.input.SetWidth(max(1, contentWidth-4))
	panelHeight := 0
	if panel := m.selectionPanelView(contentWidth); panel != "" {
		panelHeight = lipgloss.Height(panel)
	} else if panel := m.commandPanelView(contentWidth); !m.busy && panel != "" {
		panelHeight = lipgloss.Height(panel)
	}
	composerHeight := lipgloss.Height(m.inputView(contentWidth))
	footerHeight := lipgloss.Height(m.statusView(contentWidth))
	welcomeHeight := 0
	if m.welcomeVisible() {
		welcomeHeight = lipgloss.Height(m.welcomeView(contentWidth))
	}
	viewportHeight := max(1, m.height-composerHeight-footerHeight-panelHeight-welcomeHeight-2)
	m.viewport.SetWidth(contentWidth)
	m.viewport.SetHeight(viewportHeight)
}

func (m *appModel) refreshViewport() {
	wasAtBottom := m.viewport.AtBottom()
	var blocks []string
	for _, entry := range m.entries {
		if rendered := m.renderEntry(entry); rendered != "" {
			blocks = append(blocks, rendered)
		}
	}
	m.viewport.SetContent(strings.Join(blocks, "\n"))
	if wasAtBottom || m.busy {
		m.viewport.GotoBottom()
	}
}

func (m *appModel) renderEntry(entry uiEntry) string {
	width := max(1, m.viewport.Width())
	var rendered string
	switch entry.kind {
	case "user":
		rendered = renderUserCard(entry.body, width)
	case "assistant":
		rendered = renderAssistantBlock(entry.body, width, m.busy, m.animationTick)
	case "tool":
		rendered = renderToolCard(entry, width, m.expandToolOutput, m.animationTick)
	case "approval":
		rendered = renderPromptCard(entry.body, width, "approval")
	case "question":
		rendered = renderPromptCard(entry.body, width, "question")
	case "error":
		rendered = renderStatusCard(entry.body, width, "error")
	case "status":
		rendered = renderStatusCard(entry.body, width, "status")
	default:
		rendered = entry.body
	}
	return clipVisualWidth(rendered, width)
}

func renderMarkdown(content string, width int) string {
	width = max(1, width)
	renderer, err := glamour.NewTermRenderer(glamour.WithStylePath("pink"), glamour.WithWordWrap(width))
	if err != nil {
		return content
	}
	rendered, err := renderer.Render(content)
	if err != nil {
		return content
	}
	return strings.TrimSpace(rendered)
}

func (m *appModel) View() tea.View {
	terminalWidth := max(1, m.width)
	terminalHeight := max(1, m.height)
	contentWidth := m.contentWidth()
	parts := make([]string, 0, 6)
	if m.welcomeVisible() {
		welcome := m.welcomeView(contentWidth)
		// A welcome card is intentionally rich, but on a very short terminal the
		// composer and footer must remain reachable. Clip only the decorative card;
		// no transcript content is discarded.
		available := max(0, terminalHeight-lipgloss.Height(m.inputView(contentWidth))-lipgloss.Height(m.statusView(contentWidth))-2)
		if available > 0 {
			welcome = clipVisualRows(welcome, available)
			parts = append(parts, welcome)
		}
	} else {
		parts = append(parts, m.viewport.View())
	}
	if panel := m.selectionPanelView(contentWidth); panel != "" {
		parts = append(parts, panel)
	} else if panel := m.commandPanelView(contentWidth); panel != "" {
		parts = append(parts, panel)
	}
	parts = append(parts, m.inputView(contentWidth), m.statusView(contentWidth))
	outerStyle := lipgloss.NewStyle().Width(terminalWidth).Height(terminalHeight)
	if terminalWidth >= 4 {
		outerStyle = outerStyle.PaddingLeft(1).PaddingRight(1)
	}
	composed := clipVisualRowsTail(strings.Join(parts, "\n"), terminalHeight)
	content := outerStyle.Render(composed)
	// Lipgloss deliberately refuses impossible combinations such as a one-cell
	// box with two padding cells.  Clip the composed result as a final guard so
	// a tiny terminal never receives bytes wider than its reported viewport.
	content = clipVisualWidth(content, terminalWidth)
	content = clipVisualRowsTail(content, terminalHeight)
	view := tea.NewView(content)
	view.AltScreen = true
	view.MouseMode = tea.MouseModeCellMotion
	view.WindowTitle = "Banka Code"
	return view
}

func (m *appModel) contentWidth() int {
	width := max(1, m.width)
	if width < 4 {
		return width
	}
	return max(1, width-4)
}

func (m *appModel) welcomeVisible() bool {
	return len(m.entries) == 0 && m.pending == nil && !m.busy && m.input.Value() == ""
}

func (m *appModel) welcomeView(width int) string {
	panel := renderWelcomePanel(width, m.version, m.runtime, m.details, m.hitokoto, m.animationTick)
	return lipgloss.NewStyle().Width(max(1, width)).Align(lipgloss.Center).Render(panel)
}

func (m *appModel) logoView() string {
	title := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(tuiColors.text)).Render("Banka Code")
	version := lipgloss.NewStyle().Foreground(lipgloss.Color(tuiColors.inactive)).Render(" v" + m.version + "  ")
	var greeting strings.Builder
	for index, character := range []rune("Ciallo～(∠・ω< )⌒☆") {
		color := logoCharacterColor(index, m.animationTick)
		if character == '☆' && m.animationTick%10 < 2 {
			color = "#ffffff"
		}
		greeting.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Render(string(character)))
	}
	quote := ""
	if m.hitokoto != "" {
		quote = lipgloss.NewStyle().Foreground(lipgloss.Color(tuiColors.inactive)).Render("「" + m.hitokoto + "」")
	}
	return title + version + greeting.String() + "\n" + quote
}

func (m *appModel) commandPanelView(width int) string {
	if m.pending != nil || m.busy || !strings.HasPrefix(strings.TrimSpace(m.input.Value()), "/") {
		return ""
	}
	commands := m.visibleCommandSuggestions()
	if len(commands) == 0 {
		return lipgloss.NewStyle().Foreground(lipgloss.Color(tuiColors.subtle)).Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color(tuiColors.border)).PaddingLeft(1).PaddingRight(1).Width(max(8, width-2)).Render("未找到命令 · 输入 /help 查看帮助")
	}
	selected := normalizeCommandSelection(m.commandSelection, len(commands))
	lines := make([]string, 0, len(commands)+1)
	lines = append(lines, paint(tuiColors.shimmer, "命令面板")+"  "+paint(tuiColors.subtle, "↑↓选择 · Tab补全 · Enter执行"))
	for index, command := range commands {
		marker := "○ "
		style := lipgloss.NewStyle().Foreground(lipgloss.Color(tuiColors.hint))
		if index == selected {
			marker = "◉ "
			style = style.Foreground(lipgloss.Color(tuiColors.shimmer)).Bold(true)
		}
		line := fmt.Sprintf("%s%-12s %s", marker, command.Command, command.Description)
		lines = append(lines, style.Render(ansi.Truncate(line, max(8, width-6), "…")))
	}
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color(tuiColors.brand)).PaddingLeft(1).PaddingRight(1).Width(max(8, width-2)).Render(strings.Join(lines, "\n"))
}

func (m *appModel) selectionPanelView(width int) string {
	if m.pending == nil || (m.pending.approval == nil && !m.pending.permissions && m.pending.question == nil) {
		return ""
	}
	labels := make([]string, 0, 3)
	title := "需要确认"
	borderColor := tuiColors.warning
	if m.pending.permissions {
		title = "权限模式"
		for _, option := range permissionModeOptions() {
			labels = append(labels, option.label)
		}
	} else if m.pending.question != nil {
		title = "请选择一个选项"
		borderColor = tuiColors.brand
		labels = append(labels, m.pending.question.Options...)
	} else {
		for _, option := range approvalOptions() {
			labels = append(labels, option.label)
		}
	}
	selected := normalizeCommandSelection(m.pending.selection, len(labels))
	return renderOptionPanel(width, title, labels, selected, borderColor)
}

func (m *appModel) inputView(width int) string {
	innerWidth := max(8, width-2)
	row := m.input.View()
	if m.pending != nil && (m.pending.approval != nil || m.pending.permissions || (m.pending.question != nil && len(m.pending.question.Options) > 0)) && strings.TrimSpace(m.input.Value()) == "" {
		row = paint(tuiColors.hint, "↑↓ 选择 · Enter 确认 · Esc 取消")
	}
	return renderComposerRule(innerWidth, m.animationTick) + "\n" + renderComposerRow(row, innerWidth, m.busy, m.pending != nil)
}

func (m *appModel) statusView(width int) string {
	return strings.Join(renderFooterLines(width, m.runtime, m.toolContext.PermissionMode(), m.runtime.WorkspaceRoot, m.gitBranch, m.gitDirty, m.transcript, m.busy), "\n")
}

func providerLabel(provider config.ProviderKind) string {
	if provider == config.ProviderAnthropic {
		return "Anthropic"
	}
	return "OpenAI"
}

func animationTick() tea.Cmd {
	return tea.Tick(120*time.Millisecond, func(now time.Time) tea.Msg { return animationTickMsg(now) })
}

func statusMarquee(tick int) string {
	const width = 7
	period := width*2 - 2
	raw := positiveMod(tick, period)
	head := raw
	if raw > width-1 {
		head = period - raw
	}
	trail := []struct {
		glyph string
		color string
	}{
		{"●", "#ffd27a"}, {"◉", "#ffb347"}, {"◎", "#f2966b"},
		{"○", "#e07060"}, {"◌", "#a04840"}, {"·", "#602a24"},
	}
	var result strings.Builder
	for index := 0; index < width; index++ {
		distance := index - head
		if distance < 0 {
			distance = -distance
		}
		if distance < len(trail) {
			result.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(trail[distance].color)).Render(trail[distance].glyph))
		} else {
			result.WriteByte(' ')
		}
	}
	return result.String()
}

var logoPalette = []string{"#ff94b8", "#ff7cab", "#ff6699", "#ff8a66", "#ffb347", "#ff8a66", "#ff6699", "#ff7cab"}

func logoCharacterColor(index int, tick int) string {
	const waveSpan = 4.0
	cycle := float64(len(logoPalette)) + waveSpan*2
	position := math.Mod(float64(tick)*0.4-float64(index)*0.7, cycle)
	if position < 0 {
		position += cycle
	}
	if position < waveSpan {
		return interpolateHex("#b98e84", logoPalette[0], position/waveSpan)
	}
	if position < waveSpan+float64(len(logoPalette)) {
		rawIndex := position - waveSpan
		low := int(math.Floor(rawIndex))
		high := (low + 1) % len(logoPalette)
		return interpolateHex(logoPalette[low], logoPalette[high], rawIndex-float64(low))
	}
	fade := 1 - (position-waveSpan-float64(len(logoPalette)))/waveSpan
	return interpolateHex("#b98e84", logoPalette[0], max(0.0, min(1.0, fade)))
}

func interpolateHex(first string, second string, amount float64) string {
	parse := func(value string) (int, int, int) {
		number, _ := strconv.ParseInt(strings.TrimPrefix(value, "#"), 16, 64)
		return int(number >> 16 & 0xff), int(number >> 8 & 0xff), int(number & 0xff)
	}
	firstRed, firstGreen, firstBlue := parse(first)
	secondRed, secondGreen, secondBlue := parse(second)
	red := int(math.Round(float64(firstRed) + float64(secondRed-firstRed)*amount))
	green := int(math.Round(float64(firstGreen) + float64(secondGreen-firstGreen)*amount))
	blue := int(math.Round(float64(firstBlue) + float64(secondBlue-firstBlue)*amount))
	return fmt.Sprintf("#%02x%02x%02x", red, green, blue)
}
