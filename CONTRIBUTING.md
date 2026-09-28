# Contributing to Vito

Vito is open source under the [MIT License](LICENSE), and contributions are
welcome: bug reports, ideas, translation fixes and pull requests.

## Bug reports

Open an issue and say what you did, what you expected, and what happened
instead. Two things make a report much easier to act on:

- the version and commit from **Settings → About** (or `vito version`)
- your operating system, and on Linux your desktop or compositor — most of the
  platform-specific behaviour lives there

If it involves dictation going wrong, the text Vito produced and the text you
expected are worth more than a description of them.

## Ideas

Say what you are trying to do, not only what feature you think would do it. The
problem behind a request is usually the more interesting half. For anything
bigger than a small fix, opening an issue first to talk it through saves you
from writing code that goes in a different direction than Vito.

## Translations

Vito's interface ships in 60 languages, most of them machine-translated and
reviewed only lightly. If something reads badly in yours, a pull request that
fixes `web/i18n/<code>.json` is welcome — or open an issue with the string and a
better wording. How the translation files fit together is described in
[web/i18n/TRANSLATING.md](web/i18n/TRANSLATING.md).

## Pull requests

1. Fork the repository and create a branch from `main`.
2. Keep a PR to one change; small PRs are reviewed and merged faster.
3. Before pushing, make sure these pass — CI runs the same checks:

   ```sh
   gofmt -l .        # must print nothing
   go vet ./...
   go build ./...
   go test ./...
   ```

   `git config core.hooksPath .githooks` runs them automatically on every push.
4. **User-facing text:** English is the source language and the English string
   is the lookup key (`t("…")` in the web UI). Every new string needs an entry
   in the inline `TR.nl` table in `web/index.html`; filling the other languages
   is welcome but not required — missing keys fall back to English.
5. Describe what the change does and why, and how you tested it — especially on
   which OS and, on Linux, which desktop or compositor.

PRs are squash-merged, so the PR title becomes the commit message on `main`.

By submitting a pull request you agree that your contribution is licensed under
the MIT License, the same as the rest of Vito.

## Name and logo

The licence covers the code, **not** the name **Vito** or the waveform logo. A
fork is welcome under a name and an icon of its own.
