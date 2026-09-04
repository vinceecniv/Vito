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
	"regexp"
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

// DefaultRules is Vito's own cleanup prompt: the part a user may replace. It is
// exported so the settings page can show it, and offer it as the starting point
// for a rule set of their own.
const DefaultRules = `You clean up dictated speech transcripts.
- Fix punctuation and capitalization.
- Fix spelling: correct words the speech recognizer clearly garbled — non-words and obvious misspellings — to the word the speaker plainly meant from context (e.g. Dutch "gezakeld" -> "geschakeld", "uitgezakeld" -> "uitgeschakeld"). Only repair evident mis-transcriptions; never change a correctly spelled word, swap in a different word, or rephrase.
- Remove filler words and false starts ("eh", "uhm", "nou ja", stutters, repeated words) without changing meaning or tone.
- Apply the corrections list (misheard term -> intended term) strictly wherever a misheard term appears.
- Interpret spoken editing commands: "nieuwe regel" or "new line" becomes a newline; "nieuwe alinea" or "new paragraph" becomes a blank line. Nothing more clever than that.
- Replace spoken emoji requests with the emoji itself: "duim omhoog emoji" / "thumbs up emoji" -> 👍, "hartje" / "heart emoji" -> ❤️, "smiley" -> 🙂, and so on for any emoji named this way, in either language. Only when the speaker is clearly naming an emoji to insert — leave "ik steek mijn duim omhoog" as words.
- Keep the language of the transcript; do not translate.`

// outputContract is appended to every cleanup prompt, Vito's own and a custom
// one alike, and is the one line a user cannot edit away. Without it a model
// happily answers "Here is your cleaned text:" — and that sentence would be
// typed straight at the cursor, silently, with nothing to catch it. Custom rules
// may change what cleanup does; they may not change what it hands back.
const outputContract = "Output ONLY the cleaned text. No commentary, no quotes, no markdown fences."

// Builtin is one of Vito's own rule sets. They are always in the list, never
// editable, and each is tuned for a different job — which also makes them the
// worked examples someone starts their own set from: the difference between
// them is exactly the set of knobs worth turning.
type Builtin struct {
	ID          string
	Name        string // English; the settings page translates it
	Description string // one line, same
	Rules       string
}

// BuiltinPrefix namespaces Vito's own ids so they can never collide with a
// user's, whose ids are generated by the settings page.
const BuiltinPrefix = "vito:"

// Builtins lists them in the order the settings page shows them, the balanced
// default first.
func Builtins() []Builtin {
	return []Builtin{
		{
			ID:          BuiltinPrefix + "default",
			Name:        "Vito default",
			Description: "Balanced tidy-up: punctuation, spelling and fillers fixed, your wording left alone.",
			Rules:       DefaultRules,
		},
		{
			ID:          BuiltinPrefix + "verbatim",
			Name:        "Verbatim",
			Description: "Touches as little as possible — punctuation only, every spoken word kept.",
			Rules:       verbatimRules,
		},
		{
			ID:          BuiltinPrefix + "business",
			Name:        "Business writing",
			Description: "Rewrites spoken phrasing into clean, professional sentences.",
			Rules:       businessRules,
		},
		{
			ID:          BuiltinPrefix + "notes",
			Name:        "Notes",
			Description: "Turns dictated thinking into bullet points, with to-dos marked.",
			Rules:       notesRules,
		},
		{
			ID:          BuiltinPrefix + "messages",
			Name:        "Messages",
			Description: "Keeps it short and casual, emoji included — for chat and DMs.",
			Rules:       messagesRules,
		},
	}
}

// activeRules resolves the configured selection to the rules to use: one of
// Vito's own sets, one of the user's, or — for an empty or unknown id — the
// default. An id that no longer exists must land on the default rather than on
// no rules at all, which would clean up with nothing but the output contract.
func activeRules(cfg config.Cleanup) string {
	if cfg.ActivePrompt == "" {
		return ""
	}
	if r, ok := builtinRules(cfg.ActivePrompt); ok {
		return r
	}
	for _, p := range cfg.Prompts {
		if p.ID == cfg.ActivePrompt {
			return p.Rules
		}
	}
	return ""
}

// builtinRules resolves one of Vito's own ids to its rules.
func builtinRules(id string) (string, bool) {
	for _, b := range Builtins() {
		if b.ID == id {
			return b.Rules, true
		}
	}
	return "", false
}

// The lines every set below shares — the corrections list, the spoken editing
// commands and the language — are repeated rather than factored out on purpose:
// a rule set is one piece of text a user can read, copy and edit in full, and a
// set with invisible parts spliced in would not be that.

const verbatimRules = `You lightly correct dictated speech transcripts. Change as little as possible.
- Fix punctuation and capitalization. Nothing else about the wording.
- Keep every word the speaker said, including filler words, false starts, repetitions and hesitations.
- Do not fix spelling, do not swap words, do not shorten, do not rephrase, do not tidy grammar.
- Apply the corrections list (misheard term -> intended term) strictly wherever a misheard term appears.
- Interpret spoken editing commands: "nieuwe regel" or "new line" becomes a newline; "nieuwe alinea" or "new paragraph" becomes a blank line. Nothing more clever than that.
- Keep the language of the transcript; do not translate.`

const businessRules = `You turn dictated speech into clean written prose for professional use.
- Fix punctuation, capitalization and spelling.
- Remove filler words, false starts, repetitions and spoken hedging ("eh", "you know", "sort of", "eigenlijk", "gewoon").
- Rewrite spoken phrasing into written sentences: complete sentences of a readable length, no run-ons, no chatty connectors starting a sentence ("So", "And then", "Dus", "En dan").
- Keep the speaker's meaning, facts, numbers, dates and names exactly as spoken. Never add information, conclusions or pleasantries, and never drop a point.
- Use a neutral professional register — plain and direct, not stiff and not formal for its own sake.
- Apply the corrections list (misheard term -> intended term) strictly wherever a misheard term appears.
- Interpret spoken editing commands: "nieuwe regel" or "new line" becomes a newline; "nieuwe alinea" or "new paragraph" becomes a blank line.
- Keep the language of the transcript; do not translate.`

const notesRules = `You turn dictated thinking into structured notes.
- Write the content as bullet points, one idea per bullet, in the order it was spoken. Start each bullet with "- ".
- Keep bullets short: a line each, no full paragraphs. Split a long spoken sentence into several bullets when it holds several ideas.
- Start a new group after a blank line when the speaker clearly moves to another subject.
- Drop filler words, false starts and thinking out loud. Keep every fact, number, date, name, decision and open question.
- Begin a bullet with "TODO: " when the speaker framed it as a task, an action, or something to look into.
- Never invent, conclude, summarise or reorder beyond what was actually said.
- Apply the corrections list (misheard term -> intended term) strictly wherever a misheard term appears.
- Keep the language of the transcript; do not translate.`

const messagesRules = `You tidy dictated chat messages.
- Fix punctuation, capitalization and spelling, and keep the speaker's own casual wording.
- Remove filler words and false starts. Keep contractions, slang and informal phrasing exactly as spoken.
- Never make it formal, never add a greeting or a sign-off, and never expand a short message into a longer one.
- Leave a short one-line message without a closing full stop when it reads more naturally that way.
- Replace spoken emoji requests with the emoji itself: "duim omhoog emoji" / "thumbs up emoji" -> 👍, "hartje" / "heart emoji" -> ❤️, "smiley" -> 🙂, and so on for any emoji named this way, in either language. Only when the speaker is clearly naming an emoji to insert.
- Apply the corrections list (misheard term -> intended term) strictly wherever a misheard term appears.
- Interpret spoken editing commands: "nieuwe regel" or "new line" becomes a newline; "nieuwe alinea" or "new paragraph" becomes a blank line.
- Keep the language of the transcript; do not translate.`

// Contract returns the line appended to every cleanup prompt, so the settings
// page can show what it cannot edit away.
func Contract() string { return outputContract }

// cleanupPrompt builds the system prompt for a plain dictation: the given rules
// (Vito's own when empty) plus the contract that always closes it.
func cleanupPrompt(rules string) string {
	rules = strings.TrimSpace(rules)
	if rules == "" {
		rules = DefaultRules
	}
	return rules + "\n" + outputContract
}

// systemPromptWith builds the system prompt for one call: the cleanup rules, or
// — when a one-off spoken command came with it (e.g. "translate to German", from
// "Vito, vertaal naar Duits") — an instruction-first prompt instead. The command
// may override those rules, including keeping the language or not rephrasing,
// because the user asked for it explicitly.
//
// Note that the command path replaces the rules rather than extending them, so a
// custom rule set does not apply to a Vito Assist command. That is deliberate —
// carrying out an instruction is not tidying up — but it does surprise people,
// so the settings page says so.
func systemPromptWith(instruction, rules string) string {
	instruction = strings.TrimSpace(instruction)
	if instruction == "" {
		return cleanupPrompt(rules)
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
	rules  string // active cleanup rules; empty = Vito's own
}

func NewAnthropicCleaner(cfg config.Cleanup) *AnthropicCleaner {
	return &AnthropicCleaner{
		client: anthropic.NewClient(option.WithAPIKey(cfg.APIKey)),
		model:  cfg.Model,
		rules:  activeRules(cfg),
	}
}

func (c *AnthropicCleaner) Clean(ctx context.Context, text, language string, corrections []config.Correction, instruction string) (string, Usage, error) {
	msg, err := c.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:       anthropic.Model(c.model),
		MaxTokens:   maxTokensFor(text),
		Temperature: anthropic.Float(0),
		System:      []anthropic.TextBlockParam{{Text: systemPromptWith(instruction, c.rules)}},
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
	cleaned := stripThinking(out.String())
	if msg.StopReason == anthropic.StopReasonMaxTokens {
		return "", usage, truncatedErr(usage.OutputTokens, maxTokensFor(text))
	}
	if cleaned == "" {
		return "", usage, fmt.Errorf("cleanup returned empty text")
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
	effort  string // reasoning_effort, sent only when set
	rules   string // active cleanup rules; empty = Vito's own
	http    *http.Client
}

func NewOpenAICleaner(cfg config.Cleanup) *OpenAICleaner {
	return &OpenAICleaner{
		baseURL: cfg.OpenAIBaseURL,
		key:     cfg.OpenAIKey,
		model:   cfg.OpenAIModel,
		name:    ProviderName(cfg),
		effort:  strings.TrimSpace(cfg.ReasoningEffort),
		rules:   activeRules(cfg),
		http:    &http.Client{}, // the caller's context carries the timeout
	}
}

type openAIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAIRequest struct {
	Model       string  `json:"model"`
	Temperature float64 `json:"temperature"`
	MaxTokens   int64   `json:"max_tokens"`
	// ReasoningEffort is omitted unless configured: an endpoint that has never
	// heard of it answers 400, and most of them haven't.
	ReasoningEffort string          `json:"reasoning_effort,omitempty"`
	Messages        []openAIMessage `json:"messages"`
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
		Model:           c.model,
		Temperature:     0,
		MaxTokens:       maxTokensFor(text),
		ReasoningEffort: c.effort,
		Messages: []openAIMessage{
			{Role: "system", Content: systemPromptWith(instruction, c.rules)},
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
		// Which values reasoning_effort takes is per endpoint and per model — Groq
		// accepts "none" for Qwen but only low/medium/high for gpt-oss — so a
		// rejection here is a setting to change, not a bug. Say so, and quote the
		// endpoint: it usually lists the values it does accept.
		if c.effort != "" && strings.Contains(strings.ToLower(string(body)), "reasoning_effort") {
			return "", Usage{}, fmt.Errorf("%s rejected thinking=%q for this model: %s", c.name, c.effort, errSnippet(body))
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
	// Order matters: a truncated reply is reported as truncated, not as whatever
	// half-sentence survived the strip. Anything else would paste a fragment.
	cleaned := stripThinking(out.Choices[0].Message.Content)
	if out.Choices[0].FinishReason == "length" {
		return "", usage, truncatedErr(usage.OutputTokens, maxTokensFor(text))
	}
	if cleaned == "" {
		return "", usage, fmt.Errorf("cleanup returned empty text")
	}
	return cleaned, usage, nil
}

// truncatedErr explains a response that hit the ceiling. The token counts are
// the whole diagnosis: a model that spends thousands of them and still produces
// no answer was thinking, not writing. Raising the budget does not fix that —
// how long a model thinks has no relation to how long your dictation was, and
// measured on one short Dutch dictation Qwen3.6 has spent anywhere from 40 to
// over 2000 tokens on it. Turning the thinking off is the fix, which is what the
// setting named here does. Kept in the history entry, so it is still readable
// long after the toast is gone.
func truncatedErr(used, budget int64) error {
	return fmt.Errorf("cleanup output truncated: the model used all %d of its %d output tokens without finishing. "+
		"This is a reasoning model spending its budget thinking before it answers. "+
		"Set \"thinking\" to none/low under Settings → AI cleanup, or pick a model that doesn't think", used, budget)
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

// maxTokensFor bounds the response. The cleaned text is at most about as long as
// the input, but the budget has to cover far more than that: a reasoning model
// spends the same allowance thinking before it answers a word, and that thinking
// does not scale with the input at all. Measured on Groq for one 40-word Dutch
// dictation — gpt-oss-120b 173 tokens, gpt-oss-20b 374, qwen3.6-27b 1574 — so a
// budget sized on the input alone truncates the answer of every model but the
// smallest reasoner, and reports it as a Vito failure.
func maxTokensFor(text string) int64 {
	n := int64(len(text)/2 + reasoningHeadroom)
	if n > 4096 {
		n = 4096
	}
	return n
}

// reasoningHeadroom is the slack that isn't the answer: a thinking model's
// internal monologue, which is billed and counted like any other output token.
const reasoningHeadroom = 2048

// thinkBlock matches the <think>…</think> monologue that some reasoning models
// (Qwen among them) put inline in the message content rather than in a separate
// field. Left in, it *is* the "cleaned" text and lands on the clipboard. An
// unclosed block is a truncated one — the answer never arrived — so only a
// properly closed block is stripped, and what remains decides the outcome.
var thinkBlock = regexp.MustCompile(`(?s)<(think|thinking)>.*?</(think|thinking)>`)

// stripThinking removes those blocks and trims what is left.
func stripThinking(s string) string {
	return strings.TrimSpace(thinkBlock.ReplaceAllString(s, ""))
}
