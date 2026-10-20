package subtitles

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

const aiModel = "claude-haiku-5-5"

// aiMaxTokens bounds an answer; a request holds at most aiBatchChars of source text.
const aiMaxTokens = 8192

// Claude Haiku 5.5 prices in USD per million tokens (prompts under 100K tokens).
const (
	aiInputPrice  = 0.10
	aiOutputPrice = 0.50
)

// aiTokens is the usage of one request.
type aiTokens struct{ In, Out int64 }

// aiCallFunc sends one prompt and returns the JSON answer (tests replace it). A refused request
// returns errAIRefused, an answer cut off at the token limit errAITruncated (with what came).
type aiCallFunc func(ctx context.Context, apiKey, system, user string) (string, aiTokens, error)

// AIKeyError means the key or the account cannot be used (bad key, no credit): retrying soon is pointless.
// Kind names it for the clients' texts (aiErr*).
type AIKeyError struct {
	Msg  string
	Kind string
}

func (e AIKeyError) Error() string { return "AI translation: " + e.Msg }

const (
	aiErrNoKey    = "no_key"
	aiErrKey      = "key"      // the key is not valid
	aiErrCredit   = "credit"   // the account's credit balance is too low
	aiErrRejected = "rejected" // another refused request
)

// claudeAPIError maps an error answer of the API to a key or account error (ok false: another error).
func claudeAPIError(status int, text string) (AIKeyError, bool) {
	switch status {
	case 401, 403:
		return AIKeyError{"the Claude API key is not valid", aiErrKey}, true
	case 400:
		msg := claudeMessage(text)
		if strings.Contains(strings.ToLower(msg), "credit balance") {
			return AIKeyError{"the Claude account has no credit left", aiErrCredit}, true
		}
		return AIKeyError{"Claude rejected the request: " + truncateString(msg, 300), aiErrRejected}, true
	}
	return AIKeyError{}, false
}

var claudeMessageRe = regexp.MustCompile(`"message"\s*:\s*"((?:[^"\\]|\\.)*)"`)

// claudeMessage is the message of an API error answer, without the request line and its ids.
func claudeMessage(text string) string {
	m := claudeMessageRe.FindStringSubmatch(text)
	if m == nil {
		return text
	}
	var s string
	if json.Unmarshal([]byte(`"`+m[1]+`"`), &s) != nil {
		return m[1]
	}
	return s
}

func claudeCall(ctx context.Context, apiKey, system, user string) (string, aiTokens, error) {
	client := anthropic.NewClient(option.WithAPIKey(apiKey), option.WithMaxRetries(4))
	resp, err := client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     aiModel,
		MaxTokens: aiMaxTokens,
		System:    []anthropic.TextBlockParam{{Text: system}},
		// No thinking: a translation needs none and its tokens would be paid as output
		Thinking: anthropic.ThinkingConfigParamUnion{OfDisabled: &anthropic.ThinkingConfigDisabledParam{}},
		OutputConfig: anthropic.OutputConfigParam{
			Effort: anthropic.OutputConfigEffortMedium,
			Format: anthropic.JSONOutputFormatParam{Schema: aiSchema},
		},
		Messages: []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock(user))},
	})
	if err != nil {
		var apiErr *anthropic.Error
		if errors.As(err, &apiErr) {
			if keyErr, ok := claudeAPIError(apiErr.StatusCode, apiErr.Error()); ok {
				return "", aiTokens{}, keyErr
			}
		}
		return "", aiTokens{}, err
	}
	used := aiTokens{In: resp.Usage.InputTokens, Out: resp.Usage.OutputTokens}
	if resp.StopReason == anthropic.StopReasonRefusal {
		return "", used, errAIRefused
	}
	var b strings.Builder
	for _, block := range resp.Content {
		if t, ok := block.AsAny().(anthropic.TextBlock); ok {
			b.WriteString(t.Text)
		}
	}
	if resp.StopReason == anthropic.StopReasonMaxTokens {
		return b.String(), used, errAITruncated
	}
	return b.String(), used, nil
}
