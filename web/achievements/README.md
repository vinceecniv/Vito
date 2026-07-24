# Achievement medal images

## What ships here

Every achievement has a static medal PNG (`<id>.png`) rendered from
[Noto Emoji](https://github.com/googlefonts/noto-emoji), so medals look the same
on every platform instead of depending on the viewer's OS emoji font. The emoji
that also exist in the [Noto animated set](https://googlefonts.github.io/noto-emoji-animation/)
have a Lottie file under `lottie/<id>.json`; the medal plays it on unlock and on
hover. `lottie.min.js` is the player.

### Credits / licenses

- Medal PNGs and animations: **Noto Emoji** — static under the Apache License 2.0,
  animations under **CC BY 4.0**, © Google.
- `lottie.min.js`: **lottie-web** (light build) — **MIT License**, © Airbnb.

These are also credited on the in-app About page.

## Overriding with your own art

Each achievement can show custom art instead of the shipped Noto medal. Drop a PNG
here named for the achievement **id** and rebuild — the UI (both the page and the
unlock toast) switches that medal to your image automatically.

- **File name:** `<id>.png` (the ids are listed below, e.g. `words-10k.png`).
- **Size:** 512×512, square.
- **Background:** transparent.
- **Format:** PNG (24-bit + alpha).

The daemon serves these at `/achievements/<id>.png` and reports which ones exist,
so there are no broken-image requests for the ones you haven't made yet.

---

## One consistent style

All 42 medals should look like one set. Generate every image with the **same
style prefix and suffix**, changing only the middle "emblem" line. Copy this
around each emblem prompt:

**Style prefix**

> A single achievement medal badge, centered, glossy soft-3D enamel with a thin
> brushed-gold rim, gentle top-left lighting and a soft inner shadow, rounded
> friendly modern shapes, cohesive palette built around violet `#7C3AED` and
> coral `#FF6B5E` with warm cream accents, subtle radial glow behind the emblem —

**Emblem** (per achievement, from the table below)

> …a vintage studio microphone with soft warmth rising off it…

**Style suffix**

> — no text, no letters, no numbers, no words, flat transparent background,
> centered composition, product-render quality, consistent with a playful voice
> dictation app.

Keep the prefix/suffix **byte-for-byte identical** across all 42 — that is what
makes them a matching set. Generate at a fixed aspect ratio (1:1) and, if your
tool supports it, a fixed seed per batch for extra consistency, then export to
512×512 PNG with transparency.

---

## Per-achievement emblem prompts

### Words dictated
| id | name | emblem |
|----|------|--------|
| `words-100` | Warming Up | a vintage studio microphone with soft warmth/steam rising off it |
| `words-1k` | Finding Your Voice | an open mouth profile emitting three concentric sound waves |
| `words-10k` | Sir Speak-a-Lot | a golden trophy cup topped with a tiny knight's crown, a small microphone inside |
| `words-50k` | Voice Carries | a megaphone projecting broad radiating sound waves |
| `words-250k` | The Orator | an elegant black top hat resting beside a slim microphone |
| `words-1m` | Word Millionaire | a diamond-cut speech bubble sparkling like a gemstone |

### Speaking time
| id | name | emblem |
|----|------|--------|
| `spoken-10m` | Ten Minutes In | a classic round stopwatch, hand just past the start |
| `spoken-1h` | Hour of Power | a charged battery with a lightning bolt and tiny sound waves |
| `spoken-10h` | The Regular | a pair of cozy over-ear headphones |
| `spoken-50h` | Chatterbox | a lively burst-shaped speech bubble mid-chatter |
| `spoken-100h` | The Voice | a golden pro studio microphone on a small stand |

### Time saved
| id | name | emblem |
|----|------|--------|
| `saved-10m` | Finger Saver | a raised open hand with a tiny clock over the palm |
| `saved-1h` | The Tortoise | a friendly smiling tortoise carrying a small clock on its shell |
| `saved-10h` | Time Bender | an hourglass gently curving/bending, sand flowing |
| `saved-50h` | A Weekend Back | a cozy made bed with a soft pillow, restful mood |
| `saved-200h` | Time Lord | an ornate antique alarm clock with a faint magical aura |

### Streaks
| id | name | emblem |
|----|------|--------|
| `streak-3` | Habit Forming | a small green seedling sprouting from soil |
| `streak-7` | On a Roll | a rolling flame streak with a lightning accent |
| `streak-30` | Unstoppable | a single bold confident flame |
| `streak-100` | Centurion | a roman-style shield with a laurel accent |
| `streak-365` | Year of the Voice | a ring-shaped calendar wreath with a small flame at its center |

### Best day
| id | name | emblem |
|----|------|--------|
| `day-500` | Productive Day | a bright cheerful sun with soft rays |
| `day-2k` | In the Zone | a dartboard with a dart in the bullseye |
| `day-10k` | Big Day | a small rocket lifting off with a soft plume |

### Best week
| id | name | emblem |
|----|------|--------|
| `week-2k` | Busy Week | an upward bar chart with a rising arrow |
| `week-10k` | Prolific | a neat stack of three books |
| `week-40k` | Word Factory | a stylized little factory with speech-bubble smoke |

### Activations
| id | name | emblem |
|----|------|--------|
| `act-10` | Getting the Hang of It | two rounded mixer control knobs |
| `act-100` | Trigger Happy | a big satisfying pressable round button |
| `act-1k` | Reflex | a friendly rounded robot head, alert and quick |
| `act-10k` | One with the Hotkey | a single glowing keyboard key |

### Savings
| id | name | emblem |
|----|------|--------|
| `money-25` | Pocket Change | a small drawstring money bag with a couple of coins |
| `money-100` | Nice Little Sum | a tidy stack of gold coins |
| `money-250` | Serious Savings | a small banded bundle of banknotes |
| `money-500` | The Frugal One | a classic piggy bank |
| `money-1k` | Subscription Slayer | a small sword crossed over a cut subscription card |

### Milestones (special)
| id | name | emblem |
|----|------|--------|
| `first` | Hello, Vito | a friendly waving hand beside a small five-bar audio waveform |
| `night` | Night Owl | a crescent moon with a cute perched owl |
| `early` | Early Bird | a cheerful little bird against a sunrise arc |
| `comeback` | Welcome Back | a folded award ribbon with a warm welcoming feel |
| `upload` | Bring Your Own Audio | a paperclip holding a small audio-waveform card |
| `polyglot` | Polyglot | a small globe ringed by speech bubbles in different scripts |
