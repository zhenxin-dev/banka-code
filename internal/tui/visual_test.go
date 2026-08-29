package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/zhenxin-dev/banka-code/internal/config"
	"github.com/zhenxin-dev/banka-code/internal/tools"
)

func TestWelcomePanelResponsive(t *testing.T) {
	for _, width := range []int{24, 40, 58, 80, 120} {
		model := newAppModel(context.Background(), "0.1.0", config.RuntimeConfig{
			WorkspaceRoot: "/tmp/banka-project",
			Provider:      config.ProviderOpenAIChat,
			Model:         "a-very-long-model-name-for-layout-testing",
		}, nil, tools.NewRegistry(nil), tools.Context{})
		model.details.LSP = []string{"gopls 可用（懒启动）"}
		model.details.MCP = []string{"filesystem 已连接（2 tools）"}
		model.details.Actions.SkillNames = []string{"review", "release", "test"}
		model.Update(tea.WindowSizeMsg{Width: width, Height: 30})
		view := model.View()
		if lipgloss.Height(view.Content) > 30 {
			t.Fatalf("width %d: view height %d exceeds terminal:\n%s", width, lipgloss.Height(view.Content), view.Content)
		}
		for _, line := range strings.Split(view.Content, "\n") {
			if got := lipgloss.Width(line); got > width {
				t.Fatalf("width %d: line width %d exceeds terminal: %q", width, got, line)
			}
		}
		if width >= welcomeWideBreakpoint+4 {
			for _, needle := range []string{"Welcome back!", "LSP 服务器", "MCP 服务器", "Skills"} {
				if !strings.Contains(view.Content, needle) {
					t.Fatalf("width %d: welcome panel missing %q", width, needle)
				}
			}
		}
	}
}

func TestOMPInspiredCardsAndFooterFit(t *testing.T) {
	model := newAppModel(context.Background(), "0.1.0", config.RuntimeConfig{
		WorkspaceRoot: "/tmp/banka-project",
		Provider:      config.ProviderAnthropic,
		Model:         "claude-test",
	}, nil, tools.NewRegistry(nil), tools.Context{})
	model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	model.entries = []uiEntry{
		{kind: "user", body: "请检查这个项目的状态"},
		{kind: "assistant", body: "## 已完成\n\n项目状态良好。"},
		{kind: "tool", body: "Read · README.md", detail: "读取完成", toolState: "success"},
		{kind: "status", body: "✓ 1 轮"},
	}
	model.resize()
	model.refreshViewport()
	view := model.View()
	if !strings.Contains(view.Content, "Read · README.md") || !strings.Contains(view.Content, "claude-test") {
		t.Fatalf("OMP-inspired content missing: %q", view.Content)
	}
	for _, line := range strings.Split(view.Content, "\n") {
		if got := lipgloss.Width(line); got > 80 {
			t.Fatalf("line width %d exceeds terminal: %q", got, line)
		}
	}
}

func TestWelcomePanelExtremelyNarrow(t *testing.T) {
	for _, width := range []int{1, 2, 4, 8, 12, 16, 20} {
		model := newAppModel(context.Background(), "0.1.0", config.RuntimeConfig{Model: "model"}, nil, tools.NewRegistry(nil), tools.Context{})
		model.Update(tea.WindowSizeMsg{Width: width, Height: 12})
		view := model.View()
		for _, line := range strings.Split(view.Content, "\n") {
			if got := lipgloss.Width(line); got > width {
				t.Errorf("width %d: line width %d exceeds terminal: %q", width, got, line)
			}
		}
	}
}

func TestInteractivePanelsStayWithinNarrowTerminals(t *testing.T) {
	for _, width := range []int{1, 2, 4, 8, 12, 16, 20, 24} {
		model := newAppModel(context.Background(), "0.1.0", config.RuntimeConfig{Model: "model"}, nil, tools.NewRegistry(nil), tools.Context{})
		model.Update(tea.WindowSizeMsg{Width: width, Height: 12})
		model.input.SetValue("/")
		model.resize()
		for _, line := range strings.Split(model.View().Content, "\n") {
			if got := lipgloss.Width(line); got > width {
				t.Errorf("command panel width %d: line width %d exceeds terminal: %q", width, got, line)
			}
		}
	}
}
