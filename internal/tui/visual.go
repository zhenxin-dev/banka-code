package tui

// This file contains the visual vocabulary used by the interactive screen.
// The layout borrows the information hierarchy of oh-my-pi (welcome card,
// activity cards and a compact composer), while all colors and copy remain
// Banka-specific so the screen still feels like the rest of this project.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/zhenxin-dev/banka-code/internal/config"
	"github.com/zhenxin-dev/banka-code/internal/messages"
	"github.com/zhenxin-dev/banka-code/internal/permissions"
)

const (
	welcomeMaxWidth       = 100
	welcomeWideBreakpoint = 58
	maxToolPreviewLines   = 8
	maxToolPreviewWidth   = 88
)

// bankaMark is deliberately compact: it remains legible in a narrow SSH
// terminal, and the per-glyph gradient keeps the mark recognisably Banka
// instead of copying oh-my-pi's logo.
var bankaMark = []string{
	"██████",
	"██  ██",
	"██████",
	"██  ██",
	"██  ██",
}

// renderWelcomePanel builds the initial OMP-like two-column surface. It is a
// pure renderer so terminal sizing and content can be tested without starting
// Bubble Tea.
func renderWelcomePanel(width int, version string, runtime config.RuntimeConfig, details RuntimeDetails, quote string, tick int) string {
	boxWidth := min(welcomeMaxWidth, max(4, width))
	if boxWidth < welcomeWideBreakpoint {
		return clipVisualWidth(renderWelcomeNarrow(boxWidth, version, runtime, details, quote, tick), max(1, width))
	}
	innerWidth := boxWidth - 2
	leftWidth := min(30, max(22, innerWidth*35/100))
	rightWidth := max(1, innerWidth-leftWidth-1)

	leftLines := welcomeLeftLines(leftWidth, version, runtime, tick)
	rightLines := welcomeRightLines(rightWidth, details)
	rows := max(len(leftLines), len(rightLines))

	border := lipgloss.NewStyle().Foreground(lipgloss.Color(tuiColors.border))
	h := border.Render("─")
	v := border.Render("│")
	topTitle := ansi.Truncate(" Banka Code v"+strings.TrimPrefix(strings.TrimSpace(version), "v")+" ", max(0, boxWidth-4), "")
	remaining := max(0, boxWidth-ansi.StringWidth("╭──")-ansi.StringWidth(topTitle)-ansi.StringWidth("╮"))
	top := border.Render("╭──") + lipgloss.NewStyle().Foreground(lipgloss.Color(tuiColors.inactive)).Render(topTitle) + strings.Repeat(h, remaining) + border.Render("╮")
	lines := []string{top}
	for index := 0; index < rows; index++ {
		left := fitVisual(leftLinesAt(leftLines, index), leftWidth)
		right := fitVisual(leftLinesAt(rightLines, index), rightWidth)
		lines = append(lines, v+left+v+right+v)
	}
	lines = append(lines, border.Render("╰"+strings.Repeat("─", leftWidth)+"┴"+strings.Repeat("─", rightWidth)+"╯"))
	if quote != "" {
		lines = append(lines, lipgloss.NewStyle().Foreground(lipgloss.Color(tuiColors.subtle)).Italic(true).Render("  「"+truncatePlain(quote, max(8, boxWidth-4))+"」"))
	}
	return clipVisualWidth(strings.Join(lines, "\n"), max(1, width))
}

func renderWelcomeNarrow(width int, version string, runtime config.RuntimeConfig, details RuntimeDetails, quote string, tick int) string {
	contentWidth := max(1, width-2)
	border := lipgloss.NewStyle().Foreground(lipgloss.Color(tuiColors.border))
	h := border.Render("─")
	v := border.Render("│")
	topTitle := ansi.Truncate(" Banka Code v"+strings.TrimPrefix(strings.TrimSpace(version), "v")+" ", max(0, width-3), "")
	topPrefix := border.Render("╭─")
	available := max(0, width-ansi.StringWidth("╭─")-ansi.StringWidth(topTitle)-ansi.StringWidth("╮"))
	top := topPrefix + lipgloss.NewStyle().Foreground(lipgloss.Color(tuiColors.inactive)).Render(topTitle) + strings.Repeat(h, available) + border.Render("╮")
	lines := []string{top}
	for _, line := range welcomeLeftLines(contentWidth, version, runtime, tick) {
		lines = append(lines, v+fitVisual(line, contentWidth)+v)
	}
	for _, line := range compactCapabilityLines(details) {
		lines = append(lines, v+fitVisual(line, contentWidth)+v)
	}
	lines = append(lines, border.Render("╰"+strings.Repeat("─", contentWidth)+"╯"))
	if quote != "" {
		lines = append(lines, lipgloss.NewStyle().Foreground(lipgloss.Color(tuiColors.subtle)).Italic(true).Render("  「"+truncatePlain(quote, max(8, width-4))+"」"))
	}
	return clipVisualWidth(strings.Join(lines, "\n"), max(1, width))
}

func welcomeLeftLines(width int, _ string, runtime config.RuntimeConfig, tick int) []string {
	logo := make([]string, 0, len(bankaMark))
	for row, line := range bankaMark {
		logo = append(logo, gradientLogoLine(line, row, tick))
	}
	lines := []string{"", centerVisual(paint(tuiColors.text, "Welcome back!"), width), ""}
	for _, line := range logo {
		lines = append(lines, centerVisual(line, width))
	}
	lines = append(lines, "", centerVisual(paint(tuiColors.shimmer, "Ciallo～(∠・ω< )⌒☆"), width))
	model := strings.TrimSpace(runtime.Model)
	if model == "" {
		model = "未配置模型"
	}
	provider := providerLabel(runtime.Provider)
	lines = append(lines, centerVisual(paint(tuiColors.inactive, model), width), centerVisual(paint(tuiColors.subtle, provider), width), "")
	return lines
}

func welcomeRightLines(width int, details RuntimeDetails) []string {
	lines := []string{
		" " + paint(tuiColors.shimmer, "✦ 快速提示"),
		" " + paint(tuiColors.hint, "/") + paint(tuiColors.inactive, " 查看命令与能力"),
		" " + paint(tuiColors.hint, "↑↓") + paint(tuiColors.inactive, " 浏览历史/选项"),
		" " + paint(tuiColors.hint, "Ctrl+O") + paint(tuiColors.inactive, " 展开或收起工具详情"),
		paint(tuiColors.divider, " "+strings.Repeat("─", max(1, width-1))),
		" " + paint(tuiColors.shimmer, "LSP 服务器"),
	}
	lsp := normalizeCapabilityLines(details.LSP, "尚未检测到可用服务器")
	for _, capability := range lsp {
		lines = append(lines, " "+capability)
	}
	lines = append(lines, paint(tuiColors.divider, " "+strings.Repeat("─", max(1, width-1))), " "+paint(tuiColors.shimmer, "MCP 服务器"))
	for _, capability := range normalizeCapabilityLines(details.MCP, "未配置 MCP") {
		lines = append(lines, " "+capability)
	}
	lines = append(lines, paint(tuiColors.divider, " "+strings.Repeat("─", max(1, width-1))), " "+paint(tuiColors.shimmer, "Skills"))
	names := details.Actions.SkillNames
	if len(names) == 0 && details.Skills > 0 {
		lines = append(lines, " "+paint(tuiColors.success, "●")+" "+paint(tuiColors.inactive, fmt.Sprintf("%d 个技能已发现", details.Skills)))
	} else if len(names) == 0 {
		lines = append(lines, " "+paint(tuiColors.subtle, "暂无已发现技能"))
	} else {
		for _, name := range names[:min(len(names), 3)] {
			lines = append(lines, " "+paint(tuiColors.success, "●")+" "+paint(tuiColors.inactive, "/skill:"+name))
		}
		if len(names) > 3 {
			lines = append(lines, " "+paint(tuiColors.subtle, fmt.Sprintf("… 还有 %d 个", len(names)-3)))
		}
	}
	return lines
}

func compactCapabilityLines(details RuntimeDetails) []string {
	lines := []string{"", paint(tuiColors.shimmer, "✦ 当前能力")}
	for _, item := range normalizeCapabilityLines(details.LSP, "LSP：未检测到") {
		lines = append(lines, "  "+item)
	}
	for _, item := range normalizeCapabilityLines(details.MCP, "MCP：未配置") {
		lines = append(lines, "  "+item)
	}
	skillCount := len(details.Actions.SkillNames)
	if skillCount == 0 {
		skillCount = details.Skills
	}
	if skillCount > 0 {
		lines = append(lines, fmt.Sprintf("  %s %d 个技能可用", paint(tuiColors.success, "●"), skillCount))
	} else {
		lines = append(lines, "  "+paint(tuiColors.subtle, "暂无技能"))
	}
	return lines
}

func normalizeCapabilityLines(values []string, empty string) []string {
	if len(values) == 0 {
		return []string{paint(tuiColors.subtle, empty)}
	}
	lines := make([]string, 0, min(len(values), 4))
	for _, value := range values[:min(len(values), 4)] {
		value = strings.Join(strings.Fields(value), " ")
		if value == "" {
			continue
		}
		color := tuiColors.inactive
		if strings.Contains(value, "失败") || strings.Contains(value, "不可用") || strings.Contains(value, "错误") {
			color = tuiColors.err
		} else if strings.Contains(value, "连接") || strings.Contains(value, "可用") || strings.Contains(value, "运行") {
			color = tuiColors.success
		}
		lines = append(lines, paint(color, "●")+" "+paint(tuiColors.inactive, value))
	}
	if len(lines) == 0 {
		return []string{paint(tuiColors.subtle, empty)}
	}
	return lines
}

func gradientLogoLine(line string, row int, tick int) string {
	var out strings.Builder
	index := 0
	for _, character := range line {
		if character == ' ' {
			out.WriteRune(character)
			index++
			continue
		}
		out.WriteString(paint(logoCharacterColor(index+row, tick), string(character)))
		index++
	}
	return out.String()
}

func renderUserCard(body string, width int) string {
	innerWidth := max(8, width-4)
	content := strings.TrimSpace(renderMarkdown(body, innerWidth))
	if content == "" {
		content = strings.TrimSpace(body)
	}
	lines := strings.Split(content, "\n")
	style := lipgloss.NewStyle().Foreground(lipgloss.Color(tuiColors.user)).Background(lipgloss.Color("#3a2b30"))
	rows := make([]string, 0, len(lines))
	for index, line := range lines {
		prefix := "  "
		if index == 0 {
			prefix = "◈ "
		}
		row := prefix + ansi.Truncate(strings.TrimSpace(line), max(1, innerWidth-ansi.StringWidth(prefix)), "…")
		rows = append(rows, style.Width(max(1, width-2)).PaddingLeft(1).PaddingRight(1).Render(row))
	}
	return clipVisualWidth(strings.Join(rows, "\n"), max(1, width))
}

func renderAssistantBlock(body string, width int, busy bool, tick int) string {
	if strings.TrimSpace(body) == "" {
		if !busy {
			return ""
		}
		return clipVisualWidth(lipgloss.NewStyle().Foreground(lipgloss.Color(tuiColors.shimmer)).Render("  "+thinkingGlyph(tick)+" 思考中…"), max(1, width))
	}
	rendered := strings.TrimSpace(renderMarkdown(body, max(8, width-6)))
	lines := strings.Split(rendered, "\n")
	for index, line := range lines {
		prefix := "  "
		if index == 0 {
			prefix = "✿ "
		}
		lines[index] = lipgloss.NewStyle().Foreground(lipgloss.Color(tuiColors.assistant)).Render(prefix + line)
	}
	return clipVisualWidth(strings.Join(lines, "\n"), max(1, width))
}

func renderToolCard(entry uiEntry, width int, expanded bool, tick int) string {
	if entry.kind != "tool" {
		return ""
	}
	state := entry.toolState
	if state == "" {
		state = "running"
	}
	glyph := "✓"
	color := tuiColors.success
	if state == "running" {
		glyph = thinkingGlyph(tick)
		color = tuiColors.shimmer
	} else if state == "error" {
		glyph = "×"
		color = tuiColors.err
	}
	title := entry.body
	if title == "" {
		title = "工具"
	}
	title = ansi.Truncate(strings.Join(strings.Fields(title), " "), max(8, min(maxToolPreviewWidth, width-8)), "…")
	line := "  " + paint(color, glyph) + " " + paint(tuiColors.tool, title)
	if state == "running" {
		line += " " + paint(tuiColors.subtle, "运行中")
	} else if state == "error" {
		line += " " + paint(tuiColors.err, "失败")
	}
	if strings.TrimSpace(entry.detail) != "" && !expanded {
		line += " " + paint(tuiColors.subtle, "· Ctrl+O 展开")
	}
	rows := []string{line}
	if expanded && strings.TrimSpace(entry.detail) != "" {
		detail := strings.TrimSpace(entry.detail)
		if len([]rune(detail)) > maxToolPreviewWidth*maxToolPreviewLines {
			runes := []rune(detail)
			detail = string(runes[:maxToolPreviewWidth*maxToolPreviewLines]) + "…"
		}
		detailLines := strings.Split(detail, "\n")
		for _, detailLine := range detailLines[:min(len(detailLines), maxToolPreviewLines)] {
			rows = append(rows, "    "+paint(tuiColors.subtle, ansi.Truncate(strings.TrimSpace(detailLine), max(1, width-6), "…")))
		}
	}
	return clipVisualWidth(lipgloss.NewStyle().Faint(state == "running").Render(strings.Join(rows, "\n")), max(1, width))
}

func renderStatusCard(body string, width int, kind string) string {
	color := tuiColors.status
	glyph := "·"
	if kind == "error" {
		color = tuiColors.err
		glyph = "⚠"
	} else if kind == "approval" {
		color = tuiColors.warning
		glyph = "⚠"
	} else if kind == "question" {
		color = tuiColors.shimmer
		glyph = "?"
	}
	content := strings.TrimSpace(body)
	if content == "" {
		return ""
	}
	content = ansi.Truncate(content, max(8, width-6), "…")
	return clipVisualWidth(lipgloss.NewStyle().Foreground(lipgloss.Color(color)).PaddingLeft(2).Render(glyph+" "+content), max(1, width))
}

// renderPromptCard is used for approval and question requests. OMP presents
// these as a distinct surface so an interactive request cannot be mistaken for
// ordinary assistant prose; the background tint is still Banka's warm palette.
func renderPromptCard(body string, width int, kind string) string {
	content := strings.TrimSpace(renderMarkdown(body, max(8, width-8)))
	if content == "" {
		content = strings.TrimSpace(body)
	}
	if content == "" {
		return ""
	}
	background := "#382f31"
	foreground := tuiColors.assistant
	borderColor := tuiColors.border
	if kind == "approval" {
		background = "#3e3428"
		foreground = tuiColors.warning
		borderColor = tuiColors.warning
	} else if kind == "question" {
		background = "#302d3b"
		foreground = tuiColors.shimmer
		borderColor = tuiColors.brand
	}
	style := lipgloss.NewStyle().Foreground(lipgloss.Color(foreground)).Background(lipgloss.Color(background)).Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color(borderColor)).PaddingLeft(1).PaddingRight(1).Width(max(8, width-2))
	return clipVisualWidth(style.Render(content), max(1, width))
}

func renderOptionPanel(width int, title string, labels []string, selected int, borderColor string) string {
	if len(labels) == 0 {
		return ""
	}
	innerWidth := max(8, width-6)
	lines := []string{paint(tuiColors.inactive, title)}
	for index, label := range labels {
		label = strings.Join(strings.Fields(label), " ")
		marker := "○"
		color := tuiColors.hint
		if index == selected {
			marker = "◉"
			color = tuiColors.shimmer
		}
		line := marker + " " + label
		lines = append(lines, paint(color, ansi.Truncate(line, innerWidth, "…")))
	}
	style := lipgloss.NewStyle().Foreground(lipgloss.Color(tuiColors.inactive)).Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color(borderColor)).PaddingLeft(1).PaddingRight(1).Width(max(8, width-2))
	return clipVisualWidth(style.Render(strings.Join(lines, "\n")), max(1, width))
}

func renderComposerRule(width int, tick int) string {
	width = max(1, width)
	var out strings.Builder
	for index := 0; index < width; index++ {
		color := tuiColors.border
		if index >= width/2-4 && index <= width/2+4 {
			color = logoCharacterColor(index, tick)
		}
		out.WriteString(paint(color, "─"))
	}
	return out.String()
}

func renderComposerRow(input string, width int, busy bool, pending bool) string {
	// bubbles/textinput uses zero runes as spare cells for wide placeholders;
	// they are implementation sentinels, not printable terminal data.
	input = strings.ReplaceAll(input, "\x00", " ")
	marker := "◈ "
	color := tuiColors.shimmer
	if busy {
		marker = "◇ "
		color = tuiColors.status
	}
	if pending {
		marker = "↳ "
		color = tuiColors.warning
	}
	row := paint(color, marker) + input
	return ansi.Truncate(row, max(1, width), "")
}

func renderFooterLines(width int, runtime config.RuntimeConfig, permissionMode permissions.Mode, workspace string, branch string, dirty bool, transcript []messages.Message, busy bool) []string {
	pathLabel := shortenWorkspace(workspace)
	if pathLabel == "" {
		pathLabel = "."
	}
	gitLabel := ""
	if branch != "" {
		gitGlyph := "✿"
		gitColor := tuiColors.success
		if dirty {
			gitGlyph = "✱"
			gitColor = tuiColors.warning
		}
		gitLabel = "  " + paint(gitColor, gitGlyph+" "+branch)
	}
	left := paint(tuiColors.subtle, "⌂ "+pathLabel) + gitLabel
	contextLabel := estimateContextLabel(transcript)
	right := paint(tuiColors.inactive, providerLabel(runtime.Provider)+" · "+safeModelName(runtime.Model)) + "  " + paint(tuiColors.subtle, permissionMode.Label()) + "  " + paint(tuiColors.hint, contextLabel)
	if busy {
		right += "  " + paint(tuiColors.shimmer, "处理中")
	}
	if ansi.StringWidth(left)+ansi.StringWidth(right)+3 <= width {
		return []string{fitVisual(left+strings.Repeat(" ", max(1, width-ansi.StringWidth(left)-ansi.StringWidth(right)))+right, width)}
	}
	second := right + "  " + paint(tuiColors.hint, "↑↓历史 · Ctrl+O工具 · Ctrl+C退出")
	return []string{ansi.Truncate(left, width, "…"), ansi.Truncate(second, width, "…")}
}

func estimateContextLabel(transcript []messages.Message) string {
	// The wire protocol does not expose token usage yet. Use a deliberately
	// marked estimate instead of presenting a guessed value as billing data.
	characters := 0
	for _, message := range transcript {
		characters += len([]rune(message.Content))
	}
	if characters == 0 {
		return "ctx 0"
	}
	estimated := max(1, characters/4)
	if estimated >= 1000 {
		return fmt.Sprintf("ctx ≈%.1fk", float64(estimated)/1000)
	}
	return fmt.Sprintf("ctx ≈%d", estimated)
}

func shortenWorkspace(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if home, err := os.UserHomeDir(); err == nil {
		if relative, ok := strings.CutPrefix(value, home); ok && (relative == "" || strings.HasPrefix(relative, string(filepath.Separator))) {
			value = "~" + relative
		}
	}
	return value
}

// detectGitState reads only lightweight repository metadata for the footer.
// Failure is intentionally silent: Banka remains useful in directories that
// are not Git worktrees or where Git is not installed.
func detectGitState(workspace string) (branch string, dirty bool) {
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		return "", false
	}
	branchCommand := exec.Command("git", "-C", workspace, "branch", "--show-current")
	branchOutput, err := branchCommand.Output()
	if err != nil {
		return "", false
	}
	branch = strings.TrimSpace(string(branchOutput))
	if branch == "" {
		branch = "HEAD"
	}
	statusCommand := exec.Command("git", "-C", workspace, "status", "--porcelain")
	statusOutput, statusErr := statusCommand.Output()
	if statusErr == nil {
		dirty = strings.TrimSpace(string(statusOutput)) != ""
	}
	return branch, dirty
}

func safeModelName(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	if value == "" {
		return "未配置模型"
	}
	return value
}

func thinkingGlyph(tick int) string {
	frames := []string{"·", "∘", "○", "◌", "○", "∘"}
	return frames[positiveMod(tick, len(frames))]
}

func positiveMod(value int, modulus int) int {
	if modulus <= 0 {
		return 0
	}
	value %= modulus
	if value < 0 {
		return value + modulus
	}
	return value
}

func paint(color string, value string) string {
	return lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Render(value)
}

func fitVisual(value string, width int) string {
	width = max(0, width)
	if ansi.StringWidth(value) > width {
		return ansi.Truncate(value, width, "…")
	}
	return value + strings.Repeat(" ", width-ansi.StringWidth(value))
}

func centerVisual(value string, width int) string {
	width = max(0, width)
	value = ansi.Truncate(value, width, "…")
	padding := max(0, width-ansi.StringWidth(value))
	left := padding / 2
	return strings.Repeat(" ", left) + value + strings.Repeat(" ", padding-left)
}

func truncatePlain(value string, width int) string {
	return ansi.Truncate(strings.Join(strings.Fields(value), " "), max(1, width), "…")
}

func leftLinesAt(lines []string, index int) string {
	if index < 0 || index >= len(lines) {
		return ""
	}
	return lines[index]
}

func clipVisualRows(value string, maxRows int) string {
	rows := strings.Split(value, "\n")
	if maxRows <= 0 {
		return ""
	}
	if len(rows) <= maxRows {
		return value
	}
	return strings.Join(rows[:maxRows], "\n")
}

// clipVisualRowsTail keeps the most recent rows when a composed screen is
// taller than the terminal. This ensures the composer and footer remain
// reachable in a very short terminal, while clipVisualRows is still used for
// decorative welcome content where the top is the useful part.
func clipVisualRowsTail(value string, maxRows int) string {
	rows := strings.Split(value, "\n")
	if maxRows <= 0 {
		return ""
	}
	if len(rows) <= maxRows {
		return value
	}
	return strings.Join(rows[len(rows)-maxRows:], "\n")
}

// clipVisualWidth applies an ANSI-aware width limit to every row in a block.
// Lipgloss can intentionally preserve a minimum box width when padding does
// not fit; this final guard keeps callers safe in narrow panes.
func clipVisualWidth(value string, width int) string {
	if width <= 0 || value == "" {
		return ""
	}
	value = strings.ReplaceAll(value, "\x00", " ")
	rows := strings.Split(value, "\n")
	for index, row := range rows {
		rows[index] = ansi.Truncate(row, width, "…")
	}
	return strings.Join(rows, "\n")
}
