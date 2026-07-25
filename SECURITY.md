# Security policy

Vito runs locally. Your API keys and dictation history stay on your own machine
(`~/.config/vito` on Linux, `%APPDATA%\vito` / `%LOCALAPPDATA%\vito` on Windows)
and are only ever sent to the speech-recognition and cleanup providers you
configure yourself.

## Reporting a vulnerability

Please report security issues **privately** — not in a public issue or pull
request:

- Use GitHub's **[private vulnerability reporting](https://github.com/vinceecniv/Vito/security/advisories/new)**
  (the **Report a vulnerability** button on the repository's **Security** tab).

I'll acknowledge your report as soon as I can and keep you posted on a fix.
Thanks for helping keep Vito's users safe.

## Supported versions

Vito is released on a rolling, calendar-based scheme (`year.month`). Fixes land
in the **latest release**; there is no back-porting to older builds.
