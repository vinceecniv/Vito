"""Synthesises Vito's alternative feedback-sound sets into assets/sounds/<set>/.

    python scripts/sounds.py

Each set has the same six cues as the original (assets/sounds/*.wav, which
this script leaves alone): start (rising), done (resolved), cancel (falling),
command (bright, ascending), achievement (a small celebration, ~0.85 s) and
warn (two falling notes, lower). Every clip is 48 kHz mono PCM16 and matched to
the original's loudness: the loudest 50 ms of each is scaled to the loudest
50 ms of its original, so switching sets does not change how loud Vito is.
"""
import array, math, os, random, wave

RATE = 48000
ROOT = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "assets", "sounds")
NAMES = ["start", "done", "cancel", "command", "achievement", "warn"]


def hz(note):
    """'A4' style note name to Hz."""
    names = {"C": -9, "D": -7, "E": -5, "F": -4, "G": -2, "A": 0, "B": 2}
    n, rest = note[0], note[1:]
    shift = 0
    while rest and rest[0] in "#b":
        shift += 1 if rest[0] == "#" else -1
        rest = rest[1:]
    return 440 * 2 ** ((names[n] + shift + 12 * (int(rest) - 4)) / 12)


# ---- timbres: f(freq, t, dur) -> sample, before the envelope ----
def soft(f, t, d):  # a warm mallet: the fundamental with a soft octave and a touch of the 4th
    return math.sin(2 * math.pi * f * t) + 0.18 * math.sin(4 * math.pi * f * t) * math.exp(-t * 30) \
        + 0.06 * math.sin(8 * math.pi * f * t) * math.exp(-t * 60)


def glass(f, t, d):  # a small bell: inharmonic partials, the high ones dying first
    parts = [(1, 1, 0), (2.756, 0.45, 9), (5.404, 0.22, 18), (8.933, 0.1, 30)]
    return sum(a * math.sin(2 * math.pi * f * r * t) * math.exp(-t * k) for r, a, k in parts)


def retro(f, t, d):  # an 8-bit square, band-limited so it never aliases
    s, k = 0.0, 1
    while f * k < RATE / 2.4 and k < 40:
        s += math.sin(2 * math.pi * f * k * t) / k
        k += 2
    return s * 0.8


def tick(f, t, d):  # a dry wooden tick: a pitched click with a little noise
    return math.sin(2 * math.pi * f * t) * math.exp(-t * 90) + 0.25 * (random.random() * 2 - 1) * math.exp(-t * 400)


def env(t, d, attack=0.004, decay=None):
    """Attack, then an exponential fall that reaches silence at d."""
    if t < attack:
        return t / attack
    k = decay if decay is not None else 5.0 / d
    tail = min(1.0, (d - t) / 0.01) if t > d - 0.01 else 1.0
    return math.exp(-(t - attack) * k) * max(0.0, tail)


def render(notes, timbre, length, decay=None):
    """notes: (start s, note name, duration s, gain)."""
    out = [0.0] * int(RATE * length)
    for t0, name, dur, g in notes:
        f = hz(name)
        i0 = int(t0 * RATE)
        for i in range(int(dur * RATE)):
            if i0 + i >= len(out):
                break
            t = i / RATE
            out[i0 + i] += g * env(t, dur, decay=decay) * timbre(f, t, dur)
    return out


SETS = {
    "soft": dict(timbre=soft, cues={
        "start": ([(0, "G4", .09, 1), (.05, "C5", .1, 1)], .15),
        "done": ([(0, "E5", .09, 1), (.06, "C5", .12, 1)], .18),
        "cancel": ([(0, "E4", .07, 1), (.045, "C4", .08, 1)], .13),
        "command": ([(0, "C5", .12, .9), (.07, "E5", .12, .9), (.14, "G5", .25, 1)], .4),
        "achievement": ([(0, "C5", .3, .8), (.1, "E5", .3, .8), (.2, "G5", .3, .8), (.3, "C6", .55, 1)], .85),
        "warn": ([(0, "A4", .14, 1), (.13, "E4", .17, 1)], .3),
    }),
    "glass": dict(timbre=glass, cues={
        "start": ([(0, "E6", .14, 1)], .15),
        "done": ([(0, "B5", .1, .9), (.05, "E6", .14, 1)], .19),
        "cancel": ([(0, "G5", .06, 1), (.04, "D5", .09, 1)], .13),
        "command": ([(0, "E6", .2, .8), (.08, "G#6", .2, .8), (.16, "B6", .24, 1)], .4),
        "achievement": ([(0, "E6", .5, .7), (.12, "G#6", .5, .7), (.24, "B6", .5, .7), (.36, "E7", .49, .8)], .85),
        "warn": ([(0, "C6", .15, 1), (.14, "G5", .16, 1)], .3),
    }),
    "retro": dict(timbre=retro, decay=9, cues={
        "start": ([(0, "C5", .05, 1), (.05, "G5", .07, 1)], .12),
        "done": ([(0, "G5", .05, 1), (.05, "C6", .09, 1)], .14),
        "cancel": ([(0, "G4", .05, 1), (.05, "C4", .06, 1)], .11),
        "command": ([(0, "C5", .06, 1), (.06, "E5", .06, 1), (.12, "G5", .06, 1), (.18, "C6", .2, 1)], .4),
        "achievement": ([(0, "C5", .08, 1), (.08, "E5", .08, 1), (.16, "G5", .08, 1), (.24, "C6", .08, 1),
                         (.32, "E6", .08, 1), (.4, "G6", .08, 1), (.48, "C7", .37, 1)], .85),
        "warn": ([(0, "E5", .12, 1), (.15, "A4", .15, 1)], .3),
    }),
    "tick": dict(timbre=tick, cues={
        "start": ([(0, "A5", .05, 1)], .06),
        "done": ([(0, "E6", .04, 1), (.05, "E6", .05, 1)], .11),
        "cancel": ([(0, "A4", .05, 1)], .06),
        "command": ([(0, "E6", .04, 1), (.06, "A6", .04, 1), (.12, "E7", .05, 1)], .18),
        "achievement": ([(0, "C6", .06, 1), (.09, "E6", .06, 1), (.18, "G6", .06, 1), (.27, "C7", .1, 1)], .4),
        "warn": ([(0, "D5", .06, 1), (.1, "A4", .07, 1)], .18),
    }),
}


def read(path):
    with wave.open(path) as w:
        return [x / 32768 for x in array.array("h", w.readframes(w.getnframes()))]


def loudest(x, win=int(RATE * 0.05)):
    best, acc = 0.0, 0.0
    sq = [v * v for v in x]
    for i, v in enumerate(sq):
        acc += v
        if i >= win:
            acc -= sq[i - win]
        best = max(best, acc)
    return math.sqrt(best / min(win, len(x)))


def write(path, x):
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with wave.open(path, "wb") as w:
        w.setnchannels(1); w.setsampwidth(2); w.setframerate(RATE)
        w.writeframes(array.array("h", (max(-32767, min(32767, int(round(v * 32767)))) for v in x)).tobytes())


def main():
    random.seed(7)
    for name, s in SETS.items():
        for cue in NAMES:
            notes, length = s["cues"][cue]
            x = render(notes, s["timbre"], length, s.get("decay"))
            gain = loudest(read(os.path.join(ROOT, cue + ".wav"))) / max(loudest(x), 1e-9)
            gain = min(gain, 0.8 / max(abs(v) for v in x))  # never past 0.8 of full scale
            write(os.path.join(ROOT, name, cue + ".wav"), [v * gain for v in x])
        print("wrote", name)


if __name__ == "__main__":
    main()
