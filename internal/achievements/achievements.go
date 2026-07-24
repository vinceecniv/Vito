// Package achievements defines Vito's playful milestones and decides which of
// them a set of usage figures has earned.
//
// The difficulty is a curve, not a ladder: within each group the thresholds
// climb steeply (100 → 1,000 → 10,000 → …), so the first few come quickly and
// the last few almost never — while streaks and per-week records keep an active
// user with something always just out of reach. Once earned, an achievement
// stays earned.
//
// Names are English and never translated (a pun doesn't survive machine
// translation); the one-line descriptions do translate, so each group shares a
// single template with {n} standing in for the threshold.
package achievements

// Group is a family that shares a metric and a description template. Tiers
// within a group escalate.
type Group string

const (
	GroupWords       Group = "words"
	GroupSpoken      Group = "spoken"
	GroupSaved       Group = "saved"
	GroupStreak      Group = "streak"
	GroupDay         Group = "day"
	GroupWeek        Group = "week"
	GroupActivations Group = "activations"
	GroupMoney       Group = "money"
	GroupSpecial     Group = "special"
	// GroupSupport holds the honour-system "I donated" badges. They aren't derived
	// from usage — the user ticks them themselves — so they're never evaluated.
	GroupSupport Group = "support"
)

// Def is one achievement.
type Def struct {
	ID        string `json:"id"`
	Group     Group  `json:"group"`
	Icon      string `json:"icon"`             // one emoji
	Name      string `json:"name"`             // playful, English, never translated
	Desc      string `json:"desc"`             // English source; {n} → formatted threshold
	Threshold int64  `json:"threshold"`        // tiered groups
	Flag      string `json:"flag,omitempty"`   // special only: which one-off condition
	Secret    bool   `json:"secret,omitempty"` // hidden until earned
	Manual    bool   `json:"manual,omitempty"` // ticked by the user, not derived from usage
}

// Stats are the figures the definitions are checked against — lifetime totals
// plus a few records and one-off flags.
type Stats struct {
	Words         int64
	Sentences     int64
	Activations   int64
	SpokenSeconds int64
	SavedMinutes  int64
	Uploads       int64
	BestDayWords  int64
	BestWeekWords int64
	LongestStreak int64
	Languages     int64
	// SubscriptionSavings is what Vito's bring-your-own-key model has saved you
	// against a typical paid dictation app, in whole units of your display
	// currency: the months you've had Vito at ~€15/month, minus your actual API
	// spend. Computed by the server, which knows the rates and the currency.
	SubscriptionSavings int64
	Night               bool // dictated between midnight and 5am
	Early               bool // dictated between 5am and 7am
	Comeback            bool // resumed after a gap of 30+ days
}

// Value is the current progress figure for a group (used for progress bars).
func (s Stats) Value(g Group) int64 {
	switch g {
	case GroupWords:
		return s.Words
	case GroupSpoken:
		return s.SpokenSeconds
	case GroupSaved:
		return s.SavedMinutes
	case GroupStreak:
		return s.LongestStreak
	case GroupDay:
		return s.BestDayWords
	case GroupWeek:
		return s.BestWeekWords
	case GroupActivations:
		return s.Activations
	case GroupMoney:
		return s.SubscriptionSavings
	}
	return 0
}

// Earned reports whether a definition is met. Manual badges are never derived
// from usage — they live purely in the stored unlocked set — so they are always
// "not earned" here, keeping Evaluate from auto-granting them.
func (d Def) Earned(s Stats) bool {
	if d.Manual {
		return false
	}
	if d.Group == GroupSpecial {
		switch d.Flag {
		case "first":
			return s.Activations > 0
		case "night":
			return s.Night
		case "early":
			return s.Early
		case "comeback":
			return s.Comeback
		case "upload":
			return s.Uploads > 0
		case "polyglot":
			return s.Languages >= 3
		}
		return false
	}
	return s.Value(d.Group) >= d.Threshold
}

// List is every achievement, in display order. Names are deliberately silly.
var List = []Def{
	// Support badges — ticked by hand, on your honour. Kept apart from the earned
	// milestones and shown with a little extra sparkle when unlocked.
	{ID: "donate-once", Group: GroupSupport, Icon: "☕", Name: "Coffee Angel", Desc: "Treated Vito to a coffee", Manual: true},
	{ID: "donate-monthly", Group: GroupSupport, Icon: "😇", Name: "Guardian Angel", Desc: "A monthly supporter of Vito", Manual: true},

	// Words dictated, over your whole time with Vito.
	{ID: "words-100", Group: GroupWords, Icon: "🎙️", Name: "Warming Up", Desc: "Dictated {n} words", Threshold: 100},
	{ID: "words-1k", Group: GroupWords, Icon: "🗣️", Name: "Finding Your Voice", Desc: "Dictated {n} words", Threshold: 1000},
	{ID: "words-10k", Group: GroupWords, Icon: "🏆", Name: "Sir Speak-a-Lot", Desc: "Dictated {n} words", Threshold: 10000},
	{ID: "words-50k", Group: GroupWords, Icon: "📣", Name: "Voice Carries", Desc: "Dictated {n} words", Threshold: 50000},
	{ID: "words-250k", Group: GroupWords, Icon: "🎩", Name: "The Orator", Desc: "Dictated {n} words", Threshold: 250000},
	{ID: "words-1m", Group: GroupWords, Icon: "💬", Name: "Word Millionaire", Desc: "Dictated {n} words", Threshold: 1000000},

	// Time spent speaking.
	{ID: "spoken-10m", Group: GroupSpoken, Icon: "⏱️", Name: "Ten Minutes In", Desc: "Spoke for {n}", Threshold: 600},
	{ID: "spoken-1h", Group: GroupSpoken, Icon: "🔋", Name: "Hour of Power", Desc: "Spoke for {n}", Threshold: 3600},
	{ID: "spoken-10h", Group: GroupSpoken, Icon: "🎧", Name: "The Regular", Desc: "Spoke for {n}", Threshold: 36000},
	{ID: "spoken-50h", Group: GroupSpoken, Icon: "🗯️", Name: "Chatterbox", Desc: "Spoke for {n}", Threshold: 180000},
	{ID: "spoken-100h", Group: GroupSpoken, Icon: "🎤", Name: "The Voice", Desc: "Spoke for {n}", Threshold: 360000},

	// Typing time saved (vs typing it yourself).
	{ID: "saved-10m", Group: GroupSaved, Icon: "✋", Name: "Finger Saver", Desc: "Saved {n} of typing", Threshold: 10},
	{ID: "saved-1h", Group: GroupSaved, Icon: "🐢", Name: "The Tortoise", Desc: "Saved {n} of typing", Threshold: 60},
	{ID: "saved-10h", Group: GroupSaved, Icon: "⏳", Name: "Time Bender", Desc: "Saved {n} of typing", Threshold: 600},
	{ID: "saved-50h", Group: GroupSaved, Icon: "🛌", Name: "A Weekend Back", Desc: "Saved {n} of typing", Threshold: 3000},
	{ID: "saved-200h", Group: GroupSaved, Icon: "⏰", Name: "Time Lord", Desc: "Saved {n} of typing", Threshold: 12000},

	// Consecutive days used — the one that keeps pulling.
	{ID: "streak-3", Group: GroupStreak, Icon: "🌱", Name: "Habit Forming", Desc: "Kept a {n}-day streak", Threshold: 3},
	{ID: "streak-7", Group: GroupStreak, Icon: "⚡", Name: "On a Roll", Desc: "Kept a {n}-day streak", Threshold: 7},
	{ID: "streak-30", Group: GroupStreak, Icon: "🔥", Name: "Unstoppable", Desc: "Kept a {n}-day streak", Threshold: 30},
	{ID: "streak-100", Group: GroupStreak, Icon: "🛡️", Name: "Centurion", Desc: "Kept a {n}-day streak", Threshold: 100},
	{ID: "streak-365", Group: GroupStreak, Icon: "📅", Name: "Year of the Voice", Desc: "Kept a {n}-day streak", Threshold: 365},

	// Your best single day.
	{ID: "day-500", Group: GroupDay, Icon: "☀️", Name: "Productive Day", Desc: "Dictated {n} words in a day", Threshold: 500},
	{ID: "day-2k", Group: GroupDay, Icon: "🎯", Name: "In the Zone", Desc: "Dictated {n} words in a day", Threshold: 2000},
	{ID: "day-10k", Group: GroupDay, Icon: "🚀", Name: "Big Day", Desc: "Dictated {n} words in a day", Threshold: 10000},

	// Your best week.
	{ID: "week-2k", Group: GroupWeek, Icon: "📈", Name: "Busy Week", Desc: "Dictated {n} words in a week", Threshold: 2000},
	{ID: "week-10k", Group: GroupWeek, Icon: "📚", Name: "Prolific", Desc: "Dictated {n} words in a week", Threshold: 10000},
	{ID: "week-40k", Group: GroupWeek, Icon: "🏭", Name: "Word Factory", Desc: "Dictated {n} words in a week", Threshold: 40000},

	// Times you reached for the hotkey.
	{ID: "act-10", Group: GroupActivations, Icon: "🎛️", Name: "Getting the Hang of It", Desc: "Started {n} dictations", Threshold: 10},
	{ID: "act-100", Group: GroupActivations, Icon: "🔘", Name: "Trigger Happy", Desc: "Started {n} dictations", Threshold: 100},
	{ID: "act-1k", Group: GroupActivations, Icon: "🤖", Name: "Reflex", Desc: "Started {n} dictations", Threshold: 1000},
	{ID: "act-10k", Group: GroupActivations, Icon: "⌨️", Name: "One with the Hotkey", Desc: "Started {n} dictations", Threshold: 10000},

	// What you've saved by bringing your own key instead of paying a subscription.
	// {n} is a money amount; the description leaves the baseline to the UI.
	{ID: "money-25", Group: GroupMoney, Icon: "💰", Name: "Pocket Change", Desc: "Saved {n} versus a paid subscription", Threshold: 25},
	{ID: "money-100", Group: GroupMoney, Icon: "🪙", Name: "Nice Little Sum", Desc: "Saved {n} versus a paid subscription", Threshold: 100},
	{ID: "money-250", Group: GroupMoney, Icon: "💵", Name: "Serious Savings", Desc: "Saved {n} versus a paid subscription", Threshold: 250},
	{ID: "money-500", Group: GroupMoney, Icon: "🏦", Name: "The Frugal One", Desc: "Saved {n} versus a paid subscription", Threshold: 500},
	{ID: "money-1k", Group: GroupMoney, Icon: "🤑", Name: "Subscription Slayer", Desc: "Saved {n} versus a paid subscription", Threshold: 1000},

	// One-offs. The first is a welcome; the rest stay hidden until you stumble on them.
	{ID: "first", Group: GroupSpecial, Icon: "👋", Name: "Hello, Vito", Desc: "Your first dictation", Flag: "first"},
	{ID: "night", Group: GroupSpecial, Icon: "🌙", Name: "Night Owl", Desc: "Dictated after midnight", Flag: "night", Secret: true},
	{ID: "early", Group: GroupSpecial, Icon: "🐦", Name: "Early Bird", Desc: "Dictated before 7 in the morning", Flag: "early", Secret: true},
	{ID: "comeback", Group: GroupSpecial, Icon: "🎗️", Name: "Welcome Back", Desc: "Came back after 30 quiet days", Flag: "comeback", Secret: true},
	{ID: "upload", Group: GroupSpecial, Icon: "📎", Name: "Bring Your Own Audio", Desc: "Transcribed an audio file", Flag: "upload", Secret: true},
	{ID: "polyglot", Group: GroupSpecial, Icon: "🌍", Name: "Polyglot", Desc: "Dictated in three languages", Flag: "polyglot", Secret: true},
}

// Evaluate returns the ids currently earned.
func Evaluate(s Stats) map[string]bool {
	out := make(map[string]bool, len(List))
	for _, d := range List {
		if d.Earned(s) {
			out[d.ID] = true
		}
	}
	return out
}
