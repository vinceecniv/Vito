package cleanup

import "strings"

// plainChars maps characters a model likes to emit but plain-text apps often
// cannot show — Notepad draws a box for a non-breaking hyphen — onto their
// everyday equivalents, and drops the invisible ones. En and em dashes stay:
// every font has them, and they are real punctuation.
var plainChars = strings.NewReplacer(
	"\u2010", "-", // hyphen
	"\u2011", "-", // non-breaking hyphen
	"\u00A0", " ", // no-break space
	"\u202F", " ", // narrow no-break space
	"\u2007", " ", // figure space
	"\u00AD", "", // soft hyphen
	"\u200B", "", // zero-width space
	"\u200C", "", // zero-width non-joiner
	"\u2060", "", // word joiner
	"\uFEFF", "", // zero-width no-break space / byte order mark
)

// Plain returns text with those characters replaced, for what is pasted and
// stored: the text has to come out right wherever it lands.
func Plain(text string) string { return plainChars.Replace(text) }
