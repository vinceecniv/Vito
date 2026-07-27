// Package cleanup runs the optional post-processing pass over a raw
// transcript: Claude Haiku fixes punctuation, strips fillers, applies the
// dictionary corrections and interprets spoken editing commands.
package cleanup

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
	// instruction is an optional one-off spoken command ("Vito, translate to
	// German") applied to this call only; empty for a normal cleanup.
	Clean(ctx context.Context, text, language string, corrections []config.Correction, instruction string) (string, Usage, error)
}

// NewCleaner builds the cleaner for the configured provider: Anthropic, or any
// OpenAI-compatible chat-completions endpoint (Groq, OpenAI, a local model).
func NewCleaner(cfg config.Cleanup) Cleaner {
	if cfg.Provider == "openai" {
		return NewOpenAICleaner(cfg)
	}
	return NewAnthropicCleaner(cfg)
}

// Configured reports whether the selected provider has what it needs to run: a
// key for Anthropic, an endpoint for the OpenAI-compatible one (a local model
// may need no key at all).
func Configured(cfg config.Cleanup) bool {
	if cfg.Provider == "openai" {
		return cfg.OpenAIBaseURL != ""
	}
	return cfg.APIKey != ""
}

// ProviderName is the display name used in the "out of credit" UI and logs.
func ProviderName(cfg config.Cleanup) string {
	if cfg.Provider != "openai" {
		return "Anthropic"
	}
	switch b := strings.ToLower(cfg.OpenAIBaseURL); {
	case strings.Contains(b, "groq.com"):
		return "Groq"
	case strings.Contains(b, "openai.com"):
		return "OpenAI"
	case strings.Contains(b, "localhost"), strings.Contains(b, "127.0.0.1"), strings.Contains(b, "0.0.0.0"):
		return "Local model"
	default:
		return "AI cleanup"
	}
}

// buildUserPrompt assembles the user turn shared by both providers: the language,
// the dictionary corrections and the raw transcript.
func buildUserPrompt(text, language string, corrections []config.Correction) string {
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
	return user.String()
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

// systemPromptWith appends a one-off spoken command (e.g. "translate to German",
// from "Vito, vertaal naar Duits") to the base rules for a single call. The
// command may override those rules — including keeping the language or not
// rephrasing — because the user asked for it explicitly.
func systemPromptWith(instruction string) string {
	instruction = strings.TrimSpace(instruction)
	if instruction == "" {
		return systemPrompt
	}
	// Command mode: the instruction is the primary task, not cleanup — so it is
	// instruction-first, not the cleanup prompt with a note bolted on. That lets
	// big transforms and outright actions (translate, summarise, answer a
	// question, draft a reply) win over the plain "just tidy, don't change" rules.
	return `You are given a spoken instruction and a dictated transcript. First silently tidy the transcript (punctuation, spelling, remove filler words). Then carry out the instruction on that text and output ONLY the result — no commentary, no quotes, no markdown fences.

The instruction may be a transformation (translate, summarise, turn into bullet points, reformat) or a request to act on the text (answer a question, draft a reply, explain). Do exactly what it asks, in the language it implies. If the instruction is terse (e.g. "vraag" / "question"), treat the transcript as that kind of request and respond accordingly.

Instruction: ` + instruction
}

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

func (c *AnthropicCleaner) Clean(ctx context.Context, text, language string, corrections []config.Correction, instruction string) (string, Usage, error) {
	msg, err := c.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:       anthropic.Model(c.model),
		MaxTokens:   maxTokensFor(text),
		Temperature: anthropic.Float(0),
		System:      []anthropic.TextBlockParam{{Text: systemPromptWith(instruction)}},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(buildUserPrompt(text, language, corrections))),
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

// OpenAICleaner talks to any OpenAI-compatible chat-completions endpoint — Groq,
// OpenAI, or a local model — over plain HTTP, so no per-provider SDK is needed.
// A local endpoint keeps the transcript entirely on the machine.
type OpenAICleaner struct {
	baseURL string
	key     string
	model   string
	name    string // display name for errors/credit (Groq, OpenAI, Local model, …)
	http    *http.Client
}

func NewOpenAICleaner(cfg config.Cleanup) *OpenAICleaner {
	return &OpenAICleaner{
		baseURL: cfg.OpenAIBaseURL,
		key:     cfg.OpenAIKey,
		model:   cfg.OpenAIModel,
		name:    ProviderName(cfg),
		http:    &http.Client{}, // the caller's context carries the timeout
	}
}

type openAIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAIRequest struct {
	Model       string          `json:"model"`
	Temperature float64         `json:"temperature"`
	MaxTokens   int64           `json:"max_tokens"`
	Messages    []openAIMessage `json:"messages"`
}

type openAIResponse struct {
	Choices []struct {
		Message      openAIMessage `json:"message"`
		FinishReason string        `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int64 `json:"prompt_tokens"`
		CompletionTokens int64 `json:"completion_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (c *OpenAICleaner) Clean(ctx context.Context, text, language string, corrections []config.Correction, instruction string) (string, Usage, error) {
	payload, err := json.Marshal(openAIRequest{
		Model:       c.model,
		Temperature: 0,
		MaxTokens:   maxTokensFor(text),
		Messages: []openAIMessage{
			{Role: "system", Content: systemPromptWith(instruction)},
			{Role: "user", Content: buildUserPrompt(text, language, corrections)},
		},
	})
	if err != nil {
		return "", Usage{}, err
	}
	url := strings.TrimRight(c.baseURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return "", Usage{}, fmt.Errorf("build cleanup request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.key != "" { // a local model may need no key
		req.Header.Set("Authorization", "Bearer "+c.key)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return "", Usage{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	if resp.StatusCode != http.StatusOK {
		// A depleted free tier / balance comes back as 402/429; classify it so the
		// UI can say "<provider> is out of credit" the same way Anthropic does.
		if ce := apierr.FromHTTP(c.name, resp.StatusCode, string(body)); ce != nil {
			return "", Usage{}, ce
		}
		return "", Usage{}, fmt.Errorf("%s cleanup: HTTP %d: %s", c.name, resp.StatusCode, errSnippet(body))
	}

	var out openAIResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return "", Usage{}, fmt.Errorf("%s cleanup: bad response: %w", c.name, err)
	}
	if out.Error != nil && out.Error.Message != "" {
		return "", Usage{}, fmt.Errorf("%s cleanup: %s", c.name, out.Error.Message)
	}
	if len(out.Choices) == 0 {
		return "", Usage{}, fmt.Errorf("%s cleanup returned no choices", c.name)
	}
	usage := Usage{InputTokens: out.Usage.PromptTokens, OutputTokens: out.Usage.CompletionTokens}
	cleaned := strings.TrimSpace(out.Choices[0].Message.Content)
	if cleaned == "" {
		return "", usage, fmt.Errorf("cleanup returned empty text")
	}
	if out.Choices[0].FinishReason == "length" {
		return "", usage, fmt.Errorf("cleanup output truncated (max_tokens)")
	}
	return cleaned, usage, nil
}

// errSnippet trims a response body to a short, single-line hint for an error.
func errSnippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
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
