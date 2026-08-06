package tools

import (
	"context"
	"fmt"
	"strings"
)

type askUserTool struct{}

// NewAskUserTool creates a tool that pauses the agent for user input.
func NewAskUserTool() Definition { return askUserTool{} }

func (askUserTool) Name() string { return "AskUser" }
func (askUserTool) Description() string {
	return "Ask the user one necessary question when their input is required to continue."
}
func (askUserTool) InputSchema() JSONSchema {
	return objectSchema(map[string]any{
		"question": stringSchema("A concise question for the user."),
		"options": JSONSchema{
			"type": "array", "description": "Optional short choices. The user may still enter a custom answer.",
			"items": JSONSchema{"type": "string"}, "maxItems": 5,
		},
	}, "question")
}
func (askUserTool) Execute(ctx context.Context, arguments map[string]any, toolContext Context) (Result, error) {
	question, err := requireString(arguments, "question")
	if err != nil {
		return Result{}, fmt.Errorf("AskUser tool requires a non-empty 'question' string.")
	}
	options, err := optionalStringList(arguments, "options", 5)
	if err != nil {
		return Result{}, fmt.Errorf("AskUser tool requires 'options' to be an array of at most 5 non-empty strings.")
	}
	if toolContext.Interaction == nil {
		return Result{Content: "User interaction is unavailable in this mode.", IsError: true}, nil
	}
	answer, err := toolContext.Interaction.AskUser(ctx, QuestionRequest{Question: question, Options: options})
	if err != nil {
		return Result{}, err
	}
	answer = strings.TrimSpace(answer)
	if answer == "" {
		return Result{Content: "The user did not provide an answer.", IsError: true}, nil
	}
	return Result{Content: "User answer: " + answer}, nil
}

func optionalStringList(arguments map[string]any, key string, limit int) ([]string, error) {
	value, exists := arguments[key]
	if !exists {
		return nil, nil
	}
	values, ok := value.([]any)
	if !ok || len(values) > limit {
		return nil, fmt.Errorf("invalid string list")
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		text, ok := value.(string)
		text = strings.TrimSpace(text)
		if !ok || text == "" {
			return nil, fmt.Errorf("invalid string list item")
		}
		result = append(result, text)
	}
	return result, nil
}
