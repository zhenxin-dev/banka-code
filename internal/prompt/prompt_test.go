package prompt

import (
	"strings"
	"testing"

	"github.com/zhenxin-dev/banka-code/internal/instructions"
	"github.com/zhenxin-dev/banka-code/internal/skills"
)

func TestDefaultSystemPromptSeparatesPersonaLanguageAndHarness(t *testing.T) {
	required := []string{
		"[Identity]",
		"[Language]",
		"Chinese, English, and Japanese",
		"[Agent Loop]",
		"[Context]",
		"[Tools and Permissions]",
		"[Verification and Completion]",
	}
	for _, text := range required {
		if !strings.Contains(DefaultSystemPrompt, text) {
			t.Errorf("system prompt is missing %q", text)
		}
	}
	for _, forbidden := range []string{"Reply in Chinese", "nya", "proprietress"} {
		if strings.Contains(DefaultSystemPrompt, forbidden) {
			t.Errorf("system prompt still contains %q", forbidden)
		}
	}
}

func TestBuildAppendsRepositoryInstructionsAndSkills(t *testing.T) {
	result := Build(instructions.Set{Documents: []instructions.Document{{
		Path: "/workspace/AGENTS.md", Content: "repository rule",
	}}}, skills.Catalog{Skills: []skills.Skill{{Name: "review", Description: "review code"}}})

	identityIndex := strings.Index(result, "[Identity]")
	repositoryIndex := strings.Index(result, "[Repository Instructions]")
	skillsIndex := strings.Index(result, "[Skills]")
	if identityIndex < 0 || repositoryIndex <= identityIndex || skillsIndex <= repositoryIndex {
		t.Fatalf("prompt sections are out of order: %q", result)
	}
	if !strings.Contains(result, "repository rule") || !strings.Contains(result, "review: review code") {
		t.Fatalf("prompt omitted dynamic context: %q", result)
	}
}
