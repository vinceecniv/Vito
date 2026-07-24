# Keeping the 60 UI languages up to date

Vito's interface ships in 60 languages. English is the source language: the code
calls `t("<English string>")` and that English string *is* the lookup key. Dutch
lives inline in `web/index.html` as `TR = { nl: {...} }`; the other 58 languages
are one JSON file each in this folder, fetched on demand.

A key that a language hasn't translated yet falls back to the key itself, which
is already the English text. Nothing breaks when you add a string — it is simply
not translated yet.

Strings the Go side produces and the UI runs through `t()` (the chart's axis
labels in `internal/history/stats.go`) are English for the same reason.

## The rule when you add or change UI text

Every new user-facing string needs an entry in the inline `TR.nl` table, in the
same edit — the English source string as the key, its Dutch translation as the
value. That table is the contract: the script below treats its keys as the
complete set of translatable strings, and every language file is checked
against it. A string that isn't in `TR.nl` can never be translated.

This includes `title`, `aria-label` and `data-tip-text` attributes — `applyI18n`
translates those too.

## Filling the gaps

1. **Find what's missing and write the work order:**

   ```sh
   pwsh -File scripts/i18n-missing.ps1 -Export
   ```

   It prints a per-language summary and writes `scripts/missing-keys.json`,
   listing every English source string a language is still missing.

2. **Run one subagent per language.** Each agent gets the same brief
   (`scripts/TRANSLATION-BRIEF.md`), the key list, and its own file — so the
   work is parallel, per-language decisions stay traceable, and two agents can
   never touch the same file. In practice: batches of at most 20 at a time.

   The prompt per agent is only:

   > You are adding missing UI translations to Vito, in **\<language>** (code
   > `<code>`). Read `scripts/TRANSLATION-BRIEF.md` and the key list beside it,
   > `scripts/missing-keys.json`. Follow the brief exactly. Your target file:
   > `web/i18n/<code>.json`. Report back: language code, number of keys added,
   > and anything you deliberately left in English with the reason.

   Mention right-to-left for `ar`, `he`, `fa`, `ur`: keep parentheses and Latin
   fragments (prices, `ms`, language codes) intact.

3. **Verify before committing:**

   ```sh
   pwsh -File scripts/i18n-missing.ps1
   ```

   Every language must report `missing 0`, and no file may report
   `INVALID JSON`. The script parses each file, so this catches a broken edit
   as well as a skipped key.

## Conventions the translators follow

These are in the brief, repeated here because they are decisions about the
product, not about language:

- Model names (`Universal-3.5 Pro Realtime`, `Soniox stt-rt-v5`), provider names
  and `Vito` are never translated.
- `"EN, ES, FR, DE, IT, PT"` is a list of language codes and stays as it is.
- Placeholders and symbols survive verbatim: `{n}`, `→`, `≈`, `ms`, amounts.
- `"hr"` is the unit in a price (`$0.45/hr`) — each language uses its own short
  form, which is not always an abbreviation (`Std.`, `h`, `ч`, `ساعت`, `時間`).
- "Keyterms" is Vito's term for the words you feed the recogniser. Languages
  with no established equivalent keep it; the file's existing entries decide.
- The glosses `"(Voice In, Text Out)"` and `"(Bring your own key)"` print
  after the English product terms on the About page; they are translated, with
  the parentheses kept. They are hidden when the interface is English.
- Register follows the file: several languages address the user formally where
  the English source is neutral.

## Adding a 61st language

Add the code to `UI_LANGS` in `web/index.html`, add a name entry to `LANG_DATA`
and an endonym to `LANG_ENDONYM`, add the file here, and run the script — it
picks up any `*.json` in this folder. Right-to-left scripts also go in
`RTL_LANGS`.
