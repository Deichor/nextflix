package subtitles

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

const aiModel = "claude-haiku-5-5"

// Claude Haiku 5.5 prices in USD per million tokens (prompts under 100K tokens).
const (
	aiInputPrice  = 0.10
	aiOutputPrice = 0.50
)

// aiTokens is the usage of one request.
type aiTokens struct{ In, Out int64 }

// aiCallFunc sends one prompt and returns the text answer (tests replace it).
type aiCallFunc func(ctx context.Context, apiKey, system, user string) (string, aiTokens, error)

// AIKeyError means the key or the account cannot be used (bad key, no credit): retrying soon is pointless.
type AIKeyError struct{ Msg string }

func (e AIKeyError) Error() string { return "AI translation: " + e.Msg }

func claudeCall(ctx context.Context, apiKey, system, user string) (string, aiTokens, error) {
	client := anthropic.NewClient(option.WithAPIKey(apiKey), option.WithMaxRetries(4))
	resp, err := client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     aiModel,
		MaxTokens: 8192,
		System:    []anthropic.TextBlockParam{{Text: system}},
		// No thinking: a translation needs none and its tokens would be paid as output
		Thinking: anthropic.ThinkingConfigParamUnion{OfDisabled: &anthropic.ThinkingConfigDisabledParam{}},
		Messages: []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock(user))},
	})
	if err != nil {
		var apiErr *anthropic.Error
		if errors.As(err, &apiErr) {
			switch apiErr.StatusCode {
			case 401, 403:
				return "", aiTokens{}, AIKeyError{"the Claude API key is not valid"}
			case 400:
				// e.g. a credit balance that is too low
				return "", aiTokens{}, AIKeyError{fmt.Sprintf("Claude rejected the request: %s", truncateString(apiErr.Error(), 600))}
			}
		}
		return "", aiTokens{}, err
	}
	used := aiTokens{In: resp.Usage.InputTokens, Out: resp.Usage.OutputTokens}
	if resp.StopReason == anthropic.StopReasonRefusal {
		return "", used, errors.New("AI translation: Claude declined the lines")
	}
	var b strings.Builder
	for _, block := range resp.Content {
		if t, ok := block.AsAny().(anthropic.TextBlock); ok {
			b.WriteString(t.Text)
		}
	}
	return b.String(), used, nil
}
