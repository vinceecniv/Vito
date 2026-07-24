// Package cleanup runs the optional post-processing pass over a raw
// transcript: Claude Haiku fixes punctuation, strips fillers, applies the
// dictionary corrections and interprets spoken editing commands.
package cleanup

import (
	"context"
	"fmt"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"vito/internal/apierr"
	"vito/internal/config"
)

// Usage reports the token counts of a cleanup call, for cost estimation.
type Usage struct {
	InputTokens  int64
	OutputTokens int64
}

// Cleaner keeps the daemon decoupled from the provider (see design §11).
type Cleaner interface {
	Clean(ctx context.Context, text, language string, corrections []config.Correction) (string, Usage, error)
}

const systemPrompt = `You clean up dictated speech transcripts.
- Fix punctuation and capitalization.
- Fix spelling: correct words the speech recognizer clearly garbled — non-words and obvious misspellings — to the word the speaker plainly meant from context (e.g. Dutch "gezakeld" -> "geschakeld", "uitgezakeld" -> "uitgeschakeld"). Only repair evident mis-transcriptions; never change a correctly spelled word, swap in a different word, or rephrase.
- Remove filler words and false starts ("eh", "uhm", "nou ja", stutters, repeated words) without changing meaning or tone.
- Apply the corrections list (misheard term -> intended term) strictly wherever a misheard term appears.
- Interpret spoken editing commands: "nieuwe regel" or "new line" becomes a newline; "nieuwe alinea" or "new paragraph" becomes a blank line. Nothing more clever than that.
- Replace spoken emoji requests with the emoji itself: "duim omhoog emoji" / "thumbs up emoji" -> 👍, "hartje" / "heart emoji" -> ❤️, "smiley" -> 🙂, and so on for any emoji named this way, in either language. Only when the speaker is clearly naming an emoji to insert — leave "ik steek mijn duim omhoog" as words.
- Keep the language of the transcript; do not translate.
Output ONLY the cleaned text. No commentary, no quotes, no markdown fences.`

type AnthropicCleaner struct {
	client anthropic.Client
	model  string
}

func NewAnthropicCleaner(cfg config.Cleanup) *AnthropicCleaner {
	return &AnthropicCleaner{
		client: anthropic.NewClient(option.WithAPIKey(cfg.APIKey)),
		model:  cfg.Model,
	}
}

func (c *AnthropicCleaner) Clean(ctx context.Context, text, language string, corrections []config.Correction) (string, Usage, error) {
	var user strings.Builder
	fmt.Fprintf(&user, "Language: %s\n", language)
	if len(corrections) > 0 {
		user.WriteString("Corrections (misheard -> intended):\n")
		for _, corr := range corrections {
			fmt.Fprintf(&user, "- %q -> %q\n", corr.Wrong, corr.Right)
		}
	}
	user.WriteString("Transcript:\n")
	user.WriteString(text)

	msg, err := c.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:       anthropic.Model(c.model),
		MaxTokens:   maxTokensFor(text),
		Temperature: anthropic.Float(0),
		System:      []anthropic.TextBlockParam{{Text: systemPrompt}},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(user.String())),
		},
	})
	if err != nil {
		// An empty Anthropic balance comes back as a 400 whose message says the
		// credit balance is too low; classify it so the UI can say so plainly.
		if ce := apierr.FromMessage("Anthropic", err.Error()); ce != nil {
			return "", Usage{}, ce
		}
		return "", Usage{}, err
	}
	usage := Usage{InputTokens: msg.Usage.InputTokens, OutputTokens: msg.Usage.OutputTokens}
	var out strings.Builder
	for _, block := range msg.Content {
		if b, ok := block.AsAny().(anthropic.TextBlock); ok {
			out.WriteString(b.Text)
		}
	}
	cleaned := strings.TrimSpace(out.String())
	if cleaned == "" {
		return "", usage, fmt.Errorf("cleanup returned empty text")
	}
	if msg.StopReason == anthropic.StopReasonMaxTokens {
		return "", usage, fmt.Errorf("cleanup output truncated (max_tokens)")
	}
	return cleaned, usage, nil
}

// maxTokensFor bounds the response: the cleaned text is at most about as long
// as the input, plus headroom.
func maxTokensFor(text string) int64 {
	n := int64(len(text)/2 + 256)
	if n > 4096 {
		n = 4096
	}
	return n
}
