package longrec

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"vito/internal/stt"
)

// Speakers: afterwards the whole audio is transcribed once more with speaker
// recognition (stt.TranscribeSpeakers). The document then reads per turn,
// "who said what", up to where the audio ended at that moment; anything
// recorded on later follows from the blocks as before. The turns are cleaned
// in chunks of several turns, each chunk one request with the turns as
// labelled lines, so speakers never get mixed up and a free tier is not
// flooded with one request per "yes". Names are given once and apply
// everywhere (the page, the files, a copy).

// Speakers is a session's speakers.json.
type Speakers struct {
	Provider string            `json:"provider"`
	At       time.Time         `json:"at"`
	Seconds  float64           `json:"seconds"` // the audio's length when recognised
	Turns    []stt.Turn        `json:"turns"`
	Names    map[string]string `json:"names,omitempty"` // speaker id → name
}

// SpeakerClean is one line of speakers_clean.jsonl: a chunk's turns cleaned.
// A later line for the same chunk wins.
type SpeakerClean struct {
	Chunk int      `json:"c"`
	Turns []string `json:"turns,omitempty"`
	Error string   `json:"error,omitempty"`
}

// SpeakerInfo is a speaker for the page: who, and how much they said.
type SpeakerInfo struct {
	ID      string  `json:"id"`
	Name    string  `json:"name,omitempty"`
	Words   int     `json:"words"`
	Seconds float64 `json:"seconds"`
}

// Speaker recognition states (Meta.Speakers).
const (
	SpeakersRunning = "running"
	SpeakersDone    = "done"
	SpeakersFailed  = "error"
)

const (
	turnMax  = 1500 // characters: a longer turn is split at sentences
	chunkMax = 2000 // characters of turns cleaned in one request
)

// Speakers reads the session's speakers; nil when there are none.
func (s *Store) Speakers(id string) *Speakers {
	b, err := os.ReadFile(s.path(id, "speakers.json"))
	if err != nil {
		return nil
	}
	var sp Speakers
	if json.Unmarshal(b, &sp) != nil || len(sp.Turns) == 0 {
		return nil
	}
	return &sp
}

// SaveSpeakers stores recognised turns (long ones split), forgets the cleaned
// chunks of an earlier recognition and keeps the names given before.
func (s *Store) SaveSpeakers(id, provider string, seconds float64, turns []stt.Turn) error {
	sp := Speakers{Provider: provider, At: time.Now(), Seconds: seconds, Turns: splitTurns(turns)}
	if old := s.Speakers(id); old != nil {
		sp.Names = old.Names
	}
	os.Remove(s.path(id, "speakers_clean.jsonl"))
	return s.touchAfter(id, s.writeSpeakers(id, sp))
}

func (s *Store) writeSpeakers(id string, sp Speakers) error {
	b, err := json.MarshalIndent(sp, "", " ")
	if err != nil {
		return err
	}
	return writeAtomic(s.path(id, "speakers.json"), b)
}

// RemoveSpeakers goes back to the text without speakers.
func (s *Store) RemoveSpeakers(id string) error {
	os.Remove(s.path(id, "speakers_clean.jsonl"))
	if err := os.Remove(s.path(id, "speakers.json")); err != nil && !os.IsNotExist(err) {
		return err
	}
	_, err := s.UpdateMeta(id, func(m *Meta) { m.Speakers, m.SpeakersError, m.Modified = "", "", time.Now() })
	return err
}

// NameSpeaker gives a speaker a name ("" goes back to "Speaker n").
func (s *Store) NameSpeaker(id, speaker, name string) error {
	return s.touchAfter(id, s.nameSpeaker(id, speaker, name))
}

func (s *Store) nameSpeaker(id, speaker, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sp := s.Speakers(id)
	if sp == nil {
		return fmt.Errorf("this recording has no speakers")
	}
	if sp.Names == nil {
		sp.Names = map[string]string{}
	}
	if name = strings.TrimSpace(name); name == "" {
		delete(sp.Names, speaker)
	} else {
		sp.Names[speaker] = clip(name, 60)
	}
	return s.writeSpeakers(id, *sp)
}

// SpeakerCleans reads the cleaned chunks, the last line per chunk.
func (s *Store) SpeakerCleans(id string) (map[int]SpeakerClean, error) {
	lines, err := readLines[SpeakerClean](s.path(id, "speakers_clean.jsonl"))
	if err != nil {
		return nil, err
	}
	out := map[int]SpeakerClean{}
	for _, c := range lines {
		out[c.Chunk] = c
	}
	return out, nil
}

// AddSpeakerClean appends a chunk's cleaned turns.
func (s *Store) AddSpeakerClean(id string, c SpeakerClean) error {
	return s.appendLine(id, "speakers_clean.jsonl", c)
}

// chunks groups turn indexes for cleaning: whole turns, up to chunkMax
// characters each.
func (sp *Speakers) chunks() [][2]int {
	var out [][2]int
	from, size := 0, 0
	for i, t := range sp.Turns {
		n := utf8.RuneCountInString(t.Text)
		if i > from && size+n > chunkMax {
			out = append(out, [2]int{from, i})
			from, size = i, 0
		}
		size += n
	}
	if from < len(sp.Turns) {
		out = append(out, [2]int{from, len(sp.Turns)})
	}
	return out
}

// splitTurns cuts a turn longer than turnMax at sentence ends, the times
// shared out by length.
func splitTurns(in []stt.Turn) []stt.Turn {
	var out []stt.Turn
	for _, t := range in {
		total := utf8.RuneCountInString(t.Text)
		if total <= turnMax {
			out = append(out, t)
			continue
		}
		var cur strings.Builder
		done := 0
		piece := func() {
			text := strings.TrimSpace(cur.String())
			if text == "" {
				return
			}
			n := utf8.RuneCountInString(cur.String())
			c := clock{start: t.Start, end: t.End, words: t.Words, scale: 1}
			start, end := c.at(done, total), c.at(done+n, total)
			if done+n >= total {
				end = t.End
			}
			var words []stt.WordTime
			for _, w := range t.Words {
				if w.Off >= done && w.Off < done+n {
					words = append(words, stt.WordTime{Off: w.Off - done, At: w.At})
				}
			}
			out = append(out, stt.Turn{Speaker: t.Speaker, Start: start, End: end, Text: text, Words: words})
			done += n
			cur.Reset()
		}
		for _, sn := range sentences(t.Text) {
			if cur.Len() > 0 && utf8.RuneCountInString(cur.String())+utf8.RuneCountInString(sn) > turnMax {
				piece()
			}
			cur.WriteString(sn + " ")
		}
		piece()
	}
	return out
}

// speakerRules clean a chunk of turns, one labelled line each.
const speakerRules = `You clean up part of a transcript of a conversation (a meeting, an interview, a lecture with questions). Each turn is one line that starts with its speaker's label in square brackets, such as [2].
- Output exactly the same turns, in the same order, each on its own line starting with the same label. Never merge, split, drop or add a turn, and never move words from one turn to another.
- Within a turn: fix punctuation, capitalization and spelling; correct words the speech recognizer clearly garbled to the word plainly meant from context. Never change a correctly spelled word or rephrase.
- Remove filler words, false starts, stutters and repeated words without changing meaning or tone. Resolve plain self-corrections.
- Keep every fact, number, name and statement. Never summarise, answer or add anything; a turn of a single word ("Yes.") stays.
- The first and last turn may begin or end mid-sentence: keep those words as they are.
- Apply the corrections list (misheard term -> intended term) strictly wherever a misheard term appears.
- Keep the language of the transcript; do not translate.`

var turnLine = regexp.MustCompile(`^\s*\[(\d+)\]\s*(.*)$`)

// chunkText writes turns as labelled lines.
func chunkText(turns []stt.Turn) string {
	lines := make([]string, len(turns))
	for i, t := range turns {
		lines[i] = "[" + t.Speaker + "] " + strings.Join(strings.Fields(t.Text), " ")
	}
	return strings.Join(lines, "\n")
}

// parseChunk reads the cleaned lines back; it fails when the model changed
// the turns themselves (count or speakers).
func parseChunk(out string, turns []stt.Turn) ([]string, error) {
	var got []string
	var who []string
	for _, line := range strings.Split(strings.ReplaceAll(out, "\r", ""), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		m := turnLine.FindStringSubmatch(line)
		if m == nil {
			if len(got) == 0 {
				return nil, fmt.Errorf("cleaned turns lost their labels")
			}
			got[len(got)-1] += " " + strings.TrimSpace(line) // a wrapped line
			continue
		}
		who = append(who, m[1])
		got = append(got, strings.TrimSpace(m[2]))
	}
	if len(got) != len(turns) {
		return nil, fmt.Errorf("cleaned %d turns, expected %d", len(got), len(turns))
	}
	for i, t := range turns {
		if who[i] != t.Speaker {
			return nil, fmt.Errorf("turn %d changed speaker", i+1)
		}
		if strings.TrimSpace(got[i]) == "" && strings.TrimSpace(t.Text) != "" {
			return nil, fmt.Errorf("turn %d came back empty", i+1)
		}
	}
	return got, nil
}

// speakerParas lays out the turns as paragraphs (cleaned where a chunk is),
// and counts the chunks and how many are cleaned.
func (s *Store) speakerParas(id string, sp *Speakers, raw bool) (paras []para, chunks, cleaned int) {
	cleans, _ := s.SpeakerCleans(id)
	for ci, ch := range sp.chunks() {
		chunks++
		c, ok := cleans[ci]
		good := ok && c.Error == "" && len(c.Turns) == ch[1]-ch[0]
		if good {
			cleaned++
		}
		for ti := ch[0]; ti < ch[1]; ti++ {
			t := sp.Turns[ti]
			text := t.Text
			if good && !raw {
				text = c.Turns[ti-ch[0]]
			}
			c := clock{start: t.Start, end: t.End, words: t.Words, scale: 1}
			if n := utf8.RuneCountInString(strings.TrimSpace(text)); n > 0 {
				c.scale = float64(utf8.RuneCountInString(t.Text)) / float64(n) // cleaned text: about where in the words
			}
			for _, p := range splitClock(text, c) {
				p.speaker = t.Speaker
				paras = append(paras, p)
			}
		}
	}
	return paras, chunks, cleaned
}

// speakerInfo counts what each speaker said, in the order they first speak.
func speakerInfo(sp *Speakers, items []Item) []SpeakerInfo {
	idx := map[string]int{}
	var out []SpeakerInfo
	get := func(id string) *SpeakerInfo {
		i, ok := idx[id]
		if !ok {
			i = len(out)
			idx[id] = i
			out = append(out, SpeakerInfo{ID: id, Name: sp.Names[id]})
		}
		return &out[i]
	}
	for _, t := range sp.Turns {
		get(t.Speaker).Seconds += t.End - t.Start
	}
	for _, it := range items {
		if it.Speaker == "" {
			continue
		}
		w := 0
		for _, sn := range it.Sentences {
			w += len(strings.Fields(sn.Text))
		}
		get(it.Speaker).Words += w
	}
	return out
}

// SpeakerLabel is how a paragraph's speaker is written in a file or a copy:
// the name given, else "<word> <n>" (word: "Speaker", translated).
func (d Doc) SpeakerLabel(it Item, word string) string {
	if it.Name != "" {
		return it.Name
	}
	if word == "" {
		word = "Speaker"
	}
	return word + " " + it.Speaker
}

// labels says per item which speaker label to write before it: only where
// the speaker changes (or after a header), "" elsewhere.
func (d Doc) labels(word string) []string {
	out := make([]string, len(d.Items))
	last := ""
	for i, it := range d.Items {
		if it.Kind == "header" {
			last = ""
			continue
		}
		if it.Speaker != "" && it.Speaker != last {
			out[i] = d.SpeakerLabel(it, word)
		}
		last = it.Speaker
	}
	return out
}
