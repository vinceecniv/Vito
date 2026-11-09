package longrec

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"vito/internal/cleanup"
	"vito/internal/config"
	"vito/internal/stt"
)

// CleanSettings is what the queue needs from the configuration, read anew for
// each job so a settings change applies to the next block.
type CleanSettings struct {
	Cleanup     config.Cleanup
	Language    string
	Corrections []config.Correction
	On          bool // cleanup configured and switched on for long recordings
}

// rules for a block of a long recording. Not the user's active rule set:
// those are tuned for a dictation pasted at the cursor (greetings, lists,
// emoji, chat tone), and a lecture or meeting needs none of that.
const blockRules = `You clean up one block of a long, continuous speech transcript (a lecture, a meeting or a long dictation).
- Fix punctuation, capitalization and spelling; correct words the speech recognizer clearly garbled to the word plainly meant from context. Never change a correctly spelled word or rephrase.
- Remove filler words, false starts, stutters and repeated words without changing meaning or tone.
- Resolve plain self-corrections ("at four, no wait, at five" -> "at five").
- Start a new paragraph (a blank line) where the speaker clearly moves to another point; otherwise keep paragraphs together. Add no headings, lists, greetings or sign-offs.
- Keep every fact, number, name and statement. Never summarise, shorten or add anything.
- The block may begin or end mid-sentence: keep its first and last words as they are and do not complete or close a sentence that continues in the next block.
- Apply the corrections list (misheard term -> intended term) strictly wherever a misheard term appears.
- Keep the language of the transcript; do not translate.`

// blockFrame goes with a user's own rule set: what any block of a long
// recording needs, whatever the style asked for.
const blockFrame = `

This text is one block of a long, continuous recording (a lecture, a meeting or a long dictation), cleaned block by block:
- The block may begin or end mid-sentence: keep its first and last words as they are and do not complete or close a sentence that continues in the next block.
- Keep every fact, number, name and statement. Never summarise, answer or leave anything out.
- Keep the language of the transcript; do not translate.`

// rulesFor is the rules a session's blocks are cleaned with: its chosen rule
// set with the block frame, or Vito's rules for recordings (also when the
// chosen set has since been deleted).
func (set CleanSettings) rulesFor(profile string) string {
	if profile != "" {
		for _, p := range set.Cleanup.Prompts {
			if p.ID == profile && strings.TrimSpace(p.Rules) != "" {
				return strings.TrimSpace(p.Rules) + blockFrame
			}
		}
		for _, b := range cleanup.Builtins() {
			if b.ID == profile {
				return strings.TrimSpace(b.Rules) + blockFrame
			}
		}
	}
	return blockRules
}

const titleInstruction = "Give this passage a short heading of two to six words, in the passage's own language, the way a heading in meeting notes or lecture notes would read. Output only the heading: no quotes, no full stop."

type job struct {
	id    string
	index int  // -1: titles only
	done  bool // the session is finished: propose titles even for short sections
}

// Cleaner cleans blocks one at a time — a free tier allows few requests per
// minute — backing off and retrying when a request fails.
type Cleaner struct {
	store    *Store
	settings func() CleanSettings
	log      *slog.Logger
	onChange func(id string)
	jobs     chan job
}

// NewCleaner starts the queue. onChange is told when a session's text changed.
func NewCleaner(store *Store, settings func() CleanSettings, log *slog.Logger, onChange func(id string)) *Cleaner {
	c := &Cleaner{store: store, settings: settings, log: log, onChange: onChange, jobs: make(chan job, 4096)}
	go c.run()
	return c
}

// Enqueue asks for one block to be cleaned.
func (c *Cleaner) Enqueue(id string, index int) { c.put(job{id: id, index: index}) }

// Finished asks for the headers' titles of a finished session.
func (c *Cleaner) Finished(id string) { c.put(job{id: id, index: -1, done: true}) }

// EnqueueMissing asks for every block without cleaned text, then the titles.
func (c *Cleaner) EnqueueMissing(id string) {
	blocks, _ := c.store.Blocks(id)
	cleans, _ := c.store.Cleans(id)
	for _, b := range blocks {
		if cl, ok := cleans[b.Index]; (!ok || cl.Error != "") && strings.TrimSpace(b.Text) != "" {
			c.Enqueue(id, b.Index)
		}
	}
	c.Finished(id)
}

// Reclean forgets the cleaned text and cleans the whole session again.
func (c *Cleaner) Reclean(id string) error {
	if err := c.store.ResetCleans(id); err != nil {
		return err
	}
	c.EnqueueMissing(id)
	return nil
}

func (c *Cleaner) put(j job) {
	select {
	case c.jobs <- j:
	default:
		c.log.Warn("long recording: cleanup queue full", "session", j.id)
	}
}

func (c *Cleaner) run() {
	for j := range c.jobs {
		set := c.settings()
		if !set.On {
			continue
		}
		changed := false
		if j.index >= 0 {
			changed = c.cleanBlock(j, set)
		}
		if c.titles(j, set) {
			changed = true
		}
		if changed && c.onChange != nil {
			c.onChange(j.id)
		}
	}
}

// cleanBlock cleans one block with the end of the one before as context.
func (c *Cleaner) cleanBlock(j job, set CleanSettings) bool {
	blocks, err := c.store.Blocks(j.id)
	if err != nil {
		return false
	}
	cleans, _ := c.store.Cleans(j.id)
	if cl, ok := cleans[j.index]; ok && cl.Error == "" {
		return false // already done (queued twice)
	}
	var blk *Block
	prev := ""
	for i := range blocks {
		if blocks[i].Index == j.index {
			blk = &blocks[i]
			break
		}
		if t := strings.TrimSpace(blocks[i].Text); t != "" {
			prev = t
			if cl, ok := cleans[blocks[i].Index]; ok && cl.Error == "" && strings.TrimSpace(cl.Text) != "" {
				prev = cl.Text
			}
		}
	}
	if blk == nil || strings.TrimSpace(blk.Text) == "" {
		return false
	}
	m, _ := c.store.Meta(j.id)
	rules := set.rulesFor(m.Profile)
	if prev != "" {
		rules += "\n\nFor context only — the end of the previous block, already cleaned. Never repeat or output it:\n«" + tail(prev, 500) + "»"
	}
	out, err := c.ask(j.id, set, rules, blk.Text, "")
	cl := Clean{Index: j.index, Text: cleanup.Plain(strings.TrimSpace(out))}
	if err != nil {
		cl.Error = err.Error()
		c.log.Warn("long recording: block not cleaned", "session", j.id, "block", j.index, "err", err)
	} else if !plausible(blk.Text, cl.Text) {
		cl.Error = "cleaned text much shorter than the transcript"
		c.log.Warn("long recording: cleaned block rejected", "session", j.id, "block", j.index)
	}
	if err := c.store.AddClean(j.id, cl); err != nil {
		c.log.Error("long recording: cleaned block not saved", "err", err)
	}
	if cl.Error == "" {
		words := c.store.Words(j.id)
		c.store.UpdateMeta(j.id, func(m *Meta) { m.Words = words })
	}
	return cl.Error == ""
}

// plausible rejects a "cleaned" block that lost much of what was said: a
// model that summarised or answered instead of tidying.
func plausible(raw, clean string) bool {
	r, c := len(strings.Fields(raw)), len(strings.Fields(clean))
	return c > 0 && (r < 20 || float64(c) >= 0.55*float64(r))
}

func tail(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return "…" + string(r[len(r)-n:])
}

// ask runs one cleanup request, retrying with growing pauses: a rate limit on
// a free tier clears within a minute.
func (c *Cleaner) ask(id string, set CleanSettings, rules, text, instruction string) (string, error) {
	cfg := set.Cleanup
	cfg.Enabled = true
	cfg.Prompts = append([]config.Prompt(nil), config.Prompt{ID: "vito-long-block", Name: "Long recording", Rules: rules})
	cfg.ActivePrompt = "vito-long-block"
	timeout := time.Duration(max(cfg.TimeoutMS, 60000)) * time.Millisecond
	var lastErr error
	for attempt, wait := range []time.Duration{0, 10 * time.Second, 30 * time.Second, 60 * time.Second} {
		if wait > 0 {
			time.Sleep(wait)
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		out, use, err := cleanup.NewCleaner(cfg).Clean(ctx, text, set.Language, set.Corrections, instruction)
		cancel()
		if use.InputTokens > 0 || use.OutputTokens > 0 {
			c.store.UpdateMeta(id, func(m *Meta) { m.CleanIn += use.InputTokens; m.CleanOut += use.OutputTokens })
		}
		if err == nil {
			return out, nil
		}
		lastErr = err
		c.log.Info("long recording: cleanup request failed", "attempt", attempt+1, "err", err)
	}
	return "", lastErr
}

// titles proposes a title for each header that has none, once the section
// under it holds enough text (or the session is finished).
func (c *Cleaner) titles(j job, set CleanSettings) bool {
	d, err := c.store.Doc(j.id, false)
	if err != nil {
		return false
	}
	changed := false
	for i, it := range d.Items {
		if it.Kind != "header" || strings.TrimSpace(it.Title) != "" {
			continue
		}
		var sec strings.Builder
		complete := j.done
		for _, next := range d.Items[i+1:] {
			if next.Kind == "header" {
				complete = true
				break
			}
			if sec.Len() > 0 {
				sec.WriteString(" ")
			}
			sec.WriteString(joinSentences(next.Sentences, false))
			if sec.Len() >= 1500 {
				break
			}
		}
		text := strings.TrimSpace(sec.String())
		if text == "" || (!complete && utf8.RuneCountInString(text) < 600) {
			continue
		}
		out, err := c.ask(j.id, set, "", tail(text, 1500), titleInstruction)
		if err != nil {
			continue
		}
		title := strings.Trim(strings.TrimSpace(strings.SplitN(cleanup.Plain(out), "\n", 2)[0]), `"'“”.#* `)
		if title == "" || utf8.RuneCountInString(title) > 80 {
			continue
		}
		if _, err := c.store.AddEvent(j.id, Event{Type: "title", Of: it.ID, Title: title, Auto: true}); err == nil {
			changed = true
		}
	}
	return changed
}

// Restore finishes an interrupted session: the audio after the last saved
// block, and blocks whose transcription had failed, are transcribed from the
// file; the session is then done. onBlock is told about each new block.
func Restore(store *Store, id string, t stt.Transcriber, onBlock func(Block)) (Meta, error) {
	m, err := store.Meta(id)
	if err != nil {
		return m, err
	}
	if m.State != StateInterrupted && m.State != StateDone {
		return m, fmt.Errorf("session is %s", m.State)
	}
	audioPath := store.AudioPath(id)
	total := wavSeconds(audioPath)
	blocks, err := store.Blocks(id)
	if err != nil {
		return m, err
	}
	next, covered := 0, 0.0
	var firstErr error
	for _, b := range blocks {
		next = max(next, b.Index+1)
		covered = max(covered, b.End)
		if b.Error != "" && strings.TrimSpace(b.Text) == "" {
			text, err := transcribeSlice(t, audioPath, b.Start, b.End)
			if err != nil {
				firstErr = err
				continue
			}
			b.Text, b.Error = strings.TrimSpace(text), ""
			if store.AddBlock(id, b) == nil && onBlock != nil {
				onBlock(b)
			}
		}
	}
	for from := covered; total-from > 0.5; {
		to := min(from+maxBlock, total)
		b := Block{Index: next, Start: from, End: to}
		text, err := transcribeSlice(t, audioPath, from, to)
		if err != nil {
			b.Error = err.Error()
			firstErr = err
		}
		b.Text = strings.TrimSpace(text)
		if store.AddBlock(id, b) == nil && onBlock != nil && b.Error == "" {
			onBlock(b)
		}
		next++
		from = to
	}
	words := store.Words(id)
	m, err = store.UpdateMeta(id, func(m *Meta) {
		m.State = StateDone
		m.Seconds = total
		m.Words = words
	})
	if err == nil {
		err = firstErr
	}
	return m, err
}
