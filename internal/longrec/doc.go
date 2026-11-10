package longrec

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// markBack is how far a mark reaches back: you press it after hearing
// something, so it marks the sentences of the last seconds.
const markBack = 15.0

// Sentence is a piece of text with the moment it starts. Blocks carry exact
// times; within a block a sentence's time is estimated from its position.
type Sentence struct {
	Start  float64  `json:"t"`
	Text   string   `json:"text"`
	Marked bool     `json:"marked,omitempty"`
	Marks  []string `json:"marks,omitempty"` // the ids of the marks it is in
}

// Item is a header or a paragraph of the document.
type Item struct {
	Kind      string     `json:"kind"` // "header" or "para"
	ID        string     `json:"id,omitempty"`
	Title     string     `json:"title,omitempty"`
	Auto      bool       `json:"auto,omitempty"`
	At        float64    `json:"at"`
	Sentences []Sentence `json:"sentences,omitempty"`
	// Speaker of a paragraph, once speakers are recognised: the id ("1"…)
	// and the name given to it, if any.
	Speaker string `json:"speaker,omitempty"`
	Name    string `json:"name,omitempty"`
}

// MarkInfo is a mark for the side column.
type MarkInfo struct {
	ID   string  `json:"id"`
	At   float64 `json:"at"`
	Text string  `json:"text"`
	Note string  `json:"note,omitempty"`
	// From, To: the reach set by hand (Reach), else the default around At.
	From  float64 `json:"from,omitempty"`
	To    float64 `json:"to,omitempty"`
	Reach bool    `json:"reach,omitempty"`
}

// Doc is a session as one document.
type Doc struct {
	Meta    Meta       `json:"meta"`
	Items   []Item     `json:"items"`
	Marks   []MarkInfo `json:"marks"`
	Blocks  int        `json:"blocks"`
	Cleaned int        `json:"cleaned"`
	Failed  int        `json:"failed"` // blocks without a transcript
	Raw     bool       `json:"raw"`
	// Speakers, once recognised, in the order they first speak.
	Speakers []SpeakerInfo `json:"speakers,omitempty"`
	// SpeakerWord is "Speaker" in the interface's language, for a speaker
	// without a name in the files.
	SpeakerWord string `json:"-"`
}

type header struct {
	id, title string
	auto      bool
	at        float64
	// before: the header goes before the first sentence starting at or after
	// it — a second before the key was pressed (speech runs ahead of the
	// finger), or exactly the sentence it was placed at afterwards.
	before float64
}

// Doc builds the document: cleaned text (or raw when raw is set), headers and
// marks in their places.
func (s *Store) Doc(id string, raw bool) (Doc, error) {
	m, err := s.Meta(id)
	if err != nil {
		return Doc{}, err
	}
	blocks, err := s.Blocks(id)
	if err != nil {
		return Doc{}, err
	}
	cleans, err := s.Cleans(id)
	if err != nil {
		return Doc{}, err
	}
	events, err := s.Events(id)
	if err != nil {
		return Doc{}, err
	}
	d := Doc{Meta: m, Blocks: len(blocks), Raw: raw}

	// Events: headers and marks, titles and notes applied, deletions taken out.
	deleted := map[string]bool{}
	for _, e := range events {
		if e.Type == "delete" {
			deleted[e.Of] = true
		}
	}
	var heads []*header
	byID := map[string]*header{}
	typed := map[string]bool{}
	var marks []MarkInfo
	markIdx := map[string]int{}
	for _, e := range events {
		switch e.Type {
		case "header":
			if deleted[e.ID] {
				continue
			}
			h := &header{id: e.ID, title: e.Title, at: e.At, before: e.At - 1}
			if e.From > 0 || e.To > 0 { // placed at a sentence (From = its start)
				h.before = e.From - 0.001
			}
			typed[e.ID] = e.Title != ""
			heads = append(heads, h)
			byID[e.ID] = h
		case "title":
			h := byID[e.Of]
			if h == nil {
				continue
			}
			if e.Auto {
				if !typed[e.Of] {
					h.title, h.auto = e.Title, true
				}
			} else {
				h.title, h.auto = e.Title, false
				typed[e.Of] = strings.TrimSpace(e.Title) != ""
			}
		case "mark":
			if deleted[e.ID] {
				continue
			}
			markIdx[e.ID] = len(marks)
			mk := MarkInfo{ID: e.ID, At: e.At}
			if (e.From > 0 || e.To > 0) && e.From <= e.To {
				mk.From, mk.To, mk.Reach = e.From, e.To, true
			}
			marks = append(marks, mk)
		case "range":
			if i, ok := markIdx[e.Of]; ok && e.From >= 0 && e.From <= e.To {
				marks[i].From, marks[i].To, marks[i].Reach = e.From, e.To, true
			}
		case "note":
			if i, ok := markIdx[e.Of]; ok {
				marks[i].Note = e.Text
			}
		}
	}
	sort.SliceStable(heads, func(i, j int) bool { return heads[i].before < heads[j].before })

	// Sentences with times, paragraph by paragraph: per speaker's turn up to
	// where the audio ended when speakers were recognised, then the blocks.
	var paras []para
	sp := s.Speakers(id)
	after := -1.0
	if sp != nil {
		var chunks, cleaned int
		paras, chunks, cleaned = s.speakerParas(id, sp, raw)
		d.Blocks, d.Cleaned = chunks, cleaned
		after = sp.Seconds - 0.5
	}
	for _, b := range blocks {
		if after >= 0 && b.Start < after {
			if b.Error != "" && strings.TrimSpace(b.Text) == "" {
				d.Failed++
			}
			continue
		}
		if after >= 0 {
			d.Blocks++
		}
		text := b.Text
		if strings.TrimSpace(b.Text) == "" {
			// Nothing to clean (a silent last block, a failed one): done.
			d.Cleaned++
		} else if c, ok := cleans[b.Index]; ok && c.Error == "" {
			d.Cleaned++
			if !raw && strings.TrimSpace(c.Text) != "" {
				text = c.Text
			}
		}
		if b.Error != "" && strings.TrimSpace(b.Text) == "" {
			d.Failed++
		}
		paras = append(paras, splitTimed(text, b.Start, b.End)...)
	}

	// Lay out: a header goes before the first sentence that starts at or after
	// it, and opens a new paragraph.
	hi := 0
	flushHeads := func(before float64) {
		for hi < len(heads) && heads[hi].before <= before {
			h := heads[hi]
			d.Items = append(d.Items, Item{Kind: "header", ID: h.id, Title: h.title, Auto: h.auto, At: h.at})
			hi++
		}
	}
	for _, p := range paras {
		cur := Item{Kind: "para", Speaker: p.speaker}
		if sp != nil && p.speaker != "" {
			cur.Name = sp.Names[p.speaker]
		}
		for _, s := range p.sents {
			if hi < len(heads) && heads[hi].before <= s.Start {
				if len(cur.Sentences) > 0 {
					d.Items = append(d.Items, cur)
					cur = Item{Kind: "para", Speaker: cur.Speaker, Name: cur.Name}
				}
				flushHeads(s.Start)
			}
			if len(cur.Sentences) == 0 {
				cur.At = s.Start
			}
			cur.Sentences = append(cur.Sentences, s)
		}
		if len(cur.Sentences) > 0 {
			d.Items = append(d.Items, cur)
		}
	}
	flushHeads(1e12) // headers after the last word

	// Marks: the sentence a mark falls in and those of the seconds before it.
	for mi := range marks {
		mk := &marks[mi]
		var at *Sentence // the sentence being spoken when the key was pressed
		for ii := range d.Items {
			it := &d.Items[ii]
			for si := range it.Sentences {
				s := &it.Sentences[si]
				from, to := mk.At-markBack, mk.At+0.5
				if mk.Reach {
					from, to = mk.From, mk.To+0.01
				}
				next := nextStart(d.Items, ii, si)
				if s.Start <= to && next > from {
					s.Marked = true
					s.Marks = append(s.Marks, mk.ID)
					at = s
				}
			}
		}
		if at != nil {
			mk.Text = clip(at.Text, 140)
		}
	}
	d.Marks = marks
	if sp != nil {
		d.Speakers = speakerInfo(sp, d.Items)
	}
	return d, nil
}

// nextStart is when the sentence after items[ii].Sentences[si] starts.
func nextStart(items []Item, ii, si int) float64 {
	if si+1 < len(items[ii].Sentences) {
		return items[ii].Sentences[si+1].Start
	}
	for j := ii + 1; j < len(items); j++ {
		if len(items[j].Sentences) > 0 {
			return items[j].Sentences[0].Start
		}
	}
	return 1e12
}

func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:n])) + "…"
}

// splitTimed cuts a block's text into paragraphs (blank lines) and sentences,
// each sentence timed by where it sits in the block.
type para struct {
	sents   []Sentence
	speaker string
}

func splitTimed(text string, start, end float64) []para {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	total := utf8.RuneCountInString(text)
	at := func(offset int) float64 {
		if total == 0 {
			return start
		}
		return start + (end-start)*float64(offset)/float64(total)
	}
	var out []para
	offset := 0
	for _, p := range strings.Split(text, "\n\n") {
		var sents []Sentence
		local := 0
		for _, s := range sentences(p) {
			idx := strings.Index(p[local:], s)
			if idx < 0 {
				idx = 0
			}
			pos := offset + utf8.RuneCountInString(p[:local+idx])
			local += idx + len(s)
			sents = append(sents, Sentence{Start: at(pos), Text: s})
		}
		if len(sents) > 0 {
			out = append(out, para{sents: sents})
		}
		offset += utf8.RuneCountInString(p) + 2
	}
	return out
}

// sentences splits text after . ! ? … (and a closing quote or bracket) that
// is followed by a space, and at line breaks.
func sentences(p string) []string {
	var out []string
	var b strings.Builder
	rs := []rune(p)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		if r == '\n' {
			if t := strings.TrimSpace(b.String()); t != "" {
				out = append(out, t)
			}
			b.Reset()
			continue
		}
		b.WriteRune(r)
		if strings.ContainsRune(".!?…", r) {
			j := i + 1
			for j < len(rs) && strings.ContainsRune("\"'”’)»", rs[j]) {
				b.WriteRune(rs[j])
				j++
			}
			if j >= len(rs) || unicode.IsSpace(rs[j]) {
				if t := strings.TrimSpace(b.String()); t != "" {
					out = append(out, t)
				}
				b.Reset()
				i = j - 1
			}
		}
	}
	if t := strings.TrimSpace(b.String()); t != "" {
		out = append(out, t)
	}
	return out
}

// Text joins the document as plain text, paragraphs and headers separated by
// blank lines.
func (d Doc) Text() string {
	var b strings.Builder
	labels := d.labels(d.SpeakerWord)
	for i, it := range d.Items {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		if it.Kind == "header" {
			b.WriteString(headerTitle(it))
			continue
		}
		if labels[i] != "" {
			b.WriteString(labels[i] + ": ")
		}
		b.WriteString(joinSentences(it.Sentences, false))
	}
	return b.String()
}

func headerTitle(it Item) string {
	if t := strings.TrimSpace(it.Title); t != "" {
		return t
	}
	return Clock(it.At)
}

func joinSentences(ss []Sentence, mark bool) string {
	parts := make([]string, len(ss))
	for i, s := range ss {
		parts[i] = s.Text
		if mark && s.Marked {
			parts[i] = "**" + s.Text + "**"
		}
	}
	return strings.Join(parts, " ")
}

// Clock writes seconds as 1:02:03 or 2:03.
func Clock(sec float64) string {
	t := int(sec)
	if t >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", t/3600, t/60%60, t%60)
	}
	return fmt.Sprintf("%d:%02d", t/60, t%60)
}

// Markdown writes the document with its title, headers as ##, marked
// sentences in bold and the marks listed at the end.
func (d Doc) Markdown(title, when string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n_%s · %s_\n", title, when, Clock(d.Meta.Seconds))
	labels := d.labels(d.SpeakerWord)
	for i, it := range d.Items {
		b.WriteString("\n")
		if it.Kind == "header" {
			fmt.Fprintf(&b, "## %s\n", headerTitle(it))
			continue
		}
		if labels[i] != "" {
			b.WriteString("**" + labels[i] + ":** ")
		}
		b.WriteString(joinSentences(it.Sentences, true) + "\n")
	}
	if len(d.Marks) > 0 {
		b.WriteString("\n---\n\n")
		for _, m := range d.Marks {
			line := fmt.Sprintf("- %s — %s", Clock(m.At), m.Text)
			if m.Note != "" {
				line += " (" + m.Note + ")"
			}
			b.WriteString(line + "\n")
		}
	}
	return b.String()
}

// Subtitles writes SRT, or WebVTT when vtt is set: a cue per sentence, until
// the next one starts (at most 8 seconds).
func (d Doc) Subtitles(vtt bool) string {
	var all []Sentence
	labels := d.labels(d.SpeakerWord)
	for i, it := range d.Items {
		for j, s := range it.Sentences {
			if j == 0 && labels[i] != "" {
				s.Text = labels[i] + ": " + s.Text
			}
			all = append(all, s)
		}
	}
	var b strings.Builder
	if vtt {
		b.WriteString("WEBVTT\n\n")
	}
	stamp := func(sec float64) string {
		ms := int(sec*1000 + 0.5)
		sep := ","
		if vtt {
			sep = "."
		}
		return fmt.Sprintf("%02d:%02d:%02d%s%03d", ms/3600000, ms/60000%60, ms/1000%60, sep, ms%1000)
	}
	for i, s := range all {
		end := d.Meta.Seconds
		if i+1 < len(all) {
			end = all[i+1].Start
		}
		end = min(end, s.Start+8)
		if end <= s.Start {
			end = s.Start + 1
		}
		if !vtt {
			fmt.Fprintf(&b, "%d\n", i+1)
		}
		fmt.Fprintf(&b, "%s --> %s\n%s\n\n", stamp(s.Start), stamp(end), s.Text)
	}
	return b.String()
}
