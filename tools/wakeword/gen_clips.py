"""Generate the synthetic clips a Vito wake word is trained and tested on.

Positives are the wake phrase spoken by many synthetic voices: the English,
Dutch, German and French generators mix speaker embeddings (thousands of
distinct voices), the Piper voices add a dozen languages' accents. Detection
is about the sound, not the language, so the more accents say it, the better
it holds up for a speaker of any language. Negatives are phrases that sound
like the wake phrase but are not it ("hé video", "Vito" alone, ...).

The test set uses voices that never appear in training (other Piper voices
and, with --edge, Microsoft's neural voices), so its recall says something
about voices the model has not heard. Every file name starts with its
language, so the evaluation can report per language.

Every clip is written as 16 kHz mono 16-bit WAV, which is what openWakeWord's
training code expects.

    python gen_clips.py --word hey_vito --out /mnt/e/vito-wakeword/work
"""
import argparse
import asyncio
import logging
import os
import random
import uuid
import wave
from concurrent.futures import ProcessPoolExecutor
from pathlib import Path

import numpy as np
from scipy.signal import resample_poly

log = logging.getLogger("gen")

# What a user says, spelled per language so each TTS says it naturally.
# "hey" and "hé" are the same to the ear.
WORDS = {
    "hey_vito": {
        "en": ["hey vito", "hey, vito", "hey vito!", "hey veeto", "hay vito"],
        "nl": ["hé vito", "hee vito", "hé, vito", "hey vito", "hé vito!"],
        "de": ["hey vito", "hey, vito", "hei vito"],
        "fr": ["hé vito", "hey vito", "hé, vito"],
        "es": ["ey vito", "hey vito", "ey, vito"],
        "it": ["ehi vito", "hey vito", "ehi, vito"],
        "pt": ["ei vito", "hey vito", "ei, vito"],
        "pl": ["hej vito", "hej, vito"],
        "sv": ["hej vito", "hej, vito"],
        "da": ["hej vito", "hej, vito"],
        "cs": ["hej vito", "hej, vito"],
        "tr": ["hey vito", "hey, vito"],
    },
    "oke_vito": {
        "en": ["okay vito", "ok vito", "okay, vito", "okay veeto", "okay vito!"],
        "nl": ["oké vito", "okee vito", "oké, vito", "okay vito", "oké vito!"],
        "de": ["okay vito", "okay, vito", "okeh vito"],
        "fr": ["ok vito", "okay vito", "ok, vito"],
        "es": ["okey vito", "okay vito", "okey, vito"],
        "it": ["okay vito", "ok vito", "okay, vito"],
        "pt": ["okei vito", "okay vito", "okei, vito"],
        "pl": ["okej vito", "okej, vito"],
        "sv": ["okej vito", "okej, vito"],
        "da": ["okay vito", "okay, vito"],
        "cs": ["okej vito", "okej, vito"],
        "tr": ["okey vito", "okey, vito"],
    },
}

# Phrases that must not wake Vito. Dutch first: Vito's users mostly speak it,
# and openWakeWord's own adversarial generator only knows English.
NL_LEADS = ["hé", "hee", "hey", "he", "ha", "hè", "nee", "ja", "oké", "okee", "oh", "o", "nou", "zeg", "dus", "toe", "goed"]
NL_TAILS = ["video", "veto", "vita", "vitamine", "rita", "peter", "tito", "nito", "vico", "fito", "wie dan", "wie doet dat",
            "ik zie het", "vijftig", "fiets", "vinden", "weten", "voeten", "mito", "vito's", "vitaal", "vierde", "jij toch",
            "ik weet het", "wie is dat", "die ene", "dino", "pino", "gino", "kito", "bieto", "fiat", "wat doe je", "vida", "vitess"]
NL_ALONE = ["vito", "de vito", "die vito", "met vito", "vito is er", "vito zegt het", "dat is vito", "hé", "oké", "hee",
            "okee", "hé daar", "oké dan", "oké doei", "okido", "hé jij", "hé video", "oké video", "hé vitamine", "o vita",
            "video kijken", "het veto", "een vitamine", "de vita", "hier is vito", "vito komt", "vito niet"]
EN_ALONE = ["vito", "video", "veto", "hey", "okay", "hey video", "okay video", "hey there", "okay then", "hey you",
            "hey peter", "hey rita", "hey tito", "okay fido", "hey beto", "hey neat", "a video", "the veto", "hey dito"]

# Piper voices; the test voices never appear in training.
TRAIN_VOICES = ["en_GB-vctk-medium", "en_US-l2arctic-medium", "en_GB-aru-medium", "nl_BE-rdh-medium",
                "nl_BE-nathalie-medium", "nl_NL-ronnie-medium", "nl_NL-mls_5809-low", "nl_NL-mls_7432-low",
                "de_DE-thorsten_emotional-medium", "fr_FR-upmc-medium", "fr_FR-tom-medium", "es_ES-sharvard-medium",
                "es_MX-ald-medium", "it_IT-serena-medium", "pl_PL-darkman-medium", "pl_PL-mc_speech-medium",
                "pt_BR-faber-medium", "pt_PT-tugão-medium", "sv_SE-nst-medium", "da_DK-talesyntese-medium",
                "cs_CZ-jirka-medium", "tr_TR-dfki-medium"]
TEST_VOICES = ["nl_NL-pim-medium", "nl_NL-alex-medium", "en_US-arctic-medium", "en_GB-semaine-medium",
               "en_US-lessac-medium", "de_DE-thorsten-medium", "fr_FR-siwis-medium", "es_ES-davefx-medium",
               "it_IT-paola-medium", "pl_PL-gosia-medium", "pt_BR-cadu-medium", "sv_SE-lisa-medium"]
# Speaker-mixing generators and their share of the clips; the rest comes from TRAIN_VOICES.
GENERATORS = [("en_US-libritts_r-medium.pt", 0.30), ("nl_NL-mls-medium.pt", 0.25),
              ("de_DE-mls-medium.pt", 0.12), ("fr_FR-mls-medium.pt", 0.08)]

EDGE_VOICES = ["nl-NL-ColetteNeural", "nl-NL-FennaNeural", "nl-NL-MaartenNeural", "nl-BE-ArnaudNeural",
               "nl-BE-DenaNeural", "en-US-AriaNeural", "en-US-GuyNeural", "en-US-JennyNeural", "en-GB-RyanNeural",
               "en-GB-SoniaNeural", "en-AU-NatashaNeural", "en-IN-NeerjaNeural", "en-US-AndrewNeural",
               "de-DE-KatjaNeural", "de-DE-ConradNeural", "fr-FR-DeniseNeural", "fr-FR-HenriNeural",
               "es-ES-AlvaroNeural", "it-IT-ElsaNeural", "pl-PL-MarekNeural", "pt-BR-FranciscaNeural"]


def other_word(word):
    return [w for w in WORDS if w != word]


def negative_texts(word, n):
    texts = []
    # the other wake phrase must not wake this model
    for w in other_word(word):
        texts += WORDS[w]["en"] + WORDS[w]["nl"]
    texts += NL_ALONE + EN_ALONE
    for a in NL_LEADS:
        for b in NL_TAILS:
            texts.append(f"{a} {b}")
    try:
        from openwakeword.data import generate_adversarial_texts
        en = WORDS[word]["en"][0]
        texts += generate_adversarial_texts(input_text=en, N=max(n // 3, 100),
                                            include_partial_phrase=1.0, include_input_words=0.2)
    except Exception as e:  # the phonemizer model is a download; Dutch alone still works
        log.warning("no English adversarial texts: %s", e)
    random.shuffle(texts)
    return texts


def lang_of(name):
    """The language of a voice or a clip, from its file name."""
    n = Path(name).name
    if n.startswith("edge-"):
        n = n[5:]
    return n[:2]


def to16k(path):
    try:
        with wave.open(str(path), "rb") as w:
            sr, data = w.getframerate(), np.frombuffer(w.readframes(w.getnframes()), dtype=np.int16)
    except (EOFError, wave.Error):
        Path(path).unlink()  # cut off when a run was interrupted
        return
    if sr == 16000:
        return
    g = np.gcd(sr, 16000)
    y = resample_poly(data.astype(np.float32), 16000 // g, sr // g)
    y = np.clip(y, -32768, 32767).astype(np.int16)
    with wave.open(str(path), "wb") as w:
        w.setnchannels(1), w.setsampwidth(2), w.setframerate(16000), w.writeframes(y.tobytes())


def resample_dir(d):
    files = list(Path(d).glob("*.wav"))
    with ProcessPoolExecutor(max_workers=8) as ex:
        list(ex.map(to16k, files, chunksize=64))


def gen_pt(model, texts, out, n, batch, noise):
    from piper_sample_generator.__main__ import generate_samples
    if n <= 0:
        return
    generate_samples(text=texts, output_dir=out, model=model, max_samples=n, batch_size=batch,
                     slerp_weights=(0.3, 0.5, 0.7), length_scales=(0.75, 0.9, 1.0, 1.15, 1.3),
                     noise_scales=(noise,), noise_scale_ws=(noise,),
                     file_names=[f"{Path(model).stem}-{uuid.uuid4().hex}.wav" for _ in range(n)])


def _gen_onnx_one(args):
    model, texts, out, n, noise = args
    from piper_sample_generator.__main__ import generate_samples_onnx
    generate_samples_onnx(text=texts, output_dir=out, model=model, max_samples=n,
                          length_scales=(0.8, 1.0, 1.2), noise_scales=(noise,), noise_scale_ws=(noise,),
                          file_names=[f"{Path(model).stem}-{uuid.uuid4().hex}.wav" for _ in range(n)])


WORKERS = 6  # each Piper voice process holds ~1.5 GB


def gen_onnx(models, texts_for, out, n, noise):
    if n <= 0 or not models:
        return
    per = -(-n // len(models))
    jobs = [(m, texts_for(m), out, per, noise) for m in models]
    with ProcessPoolExecutor(max_workers=min(WORKERS, len(jobs))) as ex:
        list(ex.map(_gen_onnx_one, jobs))


def count(d):
    return len(list(Path(d).glob("*.wav"))) if Path(d).exists() else 0


async def _edge(texts, out, rates, pitches):
    import edge_tts
    os.makedirs(out, exist_ok=True)
    for v in EDGE_VOICES:
        for t in texts.get(v[:2], texts["en"]):
            for r in rates:
                for p in pitches:
                    f = Path(out) / f"edge-{v}-{uuid.uuid4().hex[:8]}.mp3"
                    try:
                        await edge_tts.Communicate(t, v, rate=r, pitch=p).save(str(f))
                    except Exception as e:
                        log.warning("edge %s: %s", v, e)
                        continue
                    wav = f.with_suffix(".wav")
                    os.system(f"ffmpeg -loglevel error -y -i '{f}' -ar 16000 -ac 1 '{wav}' && rm '{f}'")


def main():
    logging.basicConfig(level=logging.INFO, format="%(asctime)s %(message)s")
    ap = argparse.ArgumentParser()
    ap.add_argument("--word", required=True, choices=list(WORDS))
    ap.add_argument("--out", required=True)
    ap.add_argument("--voices", default="/mnt/e/vito-wakeword/data/voices")
    ap.add_argument("--n-train", type=int, default=30000)
    ap.add_argument("--n-val", type=int, default=3000)
    ap.add_argument("--batch", type=int, default=60)
    ap.add_argument("--edge", action="store_true", help="also make the held-out test set with Microsoft neural voices")
    ap.add_argument("--no-generators", action="store_true",
                    help="Piper voices only: the speaker-mixing generators need a GPU (on the CPU they take >12 GB)")
    ap.add_argument("--workers", type=int, default=6)
    a = ap.parse_args()
    global WORKERS
    WORKERS = a.workers
    gens = [] if a.no_generators else GENERATORS

    V = Path(a.voices)
    train_onnx = [str(V / f"{m}.onnx") for m in TRAIN_VOICES]
    test_onnx = [str(V / f"{m}.onnx") for m in TEST_VOICES]
    W = WORDS[a.word]
    root = Path(a.out) / a.word
    dirs = {k: root / k for k in ["positive_train", "positive_test", "negative_train", "negative_test",
                                  "heldout_positive", "heldout_negative"]}
    for d in dirs.values():
        d.mkdir(parents=True, exist_ok=True)

    negs = negative_texts(a.word, a.n_train)
    split = int(len(negs) * 0.9)
    neg_train, neg_val = negs[:split], negs[split:]
    nl_neg = [t for t in negs if t.split() and (t.split()[0] in NL_LEADS or t in NL_ALONE)]

    # (output dir, wanted, texts by language, noise)
    plan = [
        ("positive_train", a.n_train, W, 0.667),
        ("positive_test", a.n_val, W, 0.8),
        ("negative_train", a.n_train, {lang: neg_train for lang in W}, 0.667),
        ("negative_test", a.n_val, {lang: neg_val for lang in W}, 0.8),
    ]
    for name, n, texts, noise in plan:
        d = dirs[name]
        have = count(d)
        if have >= 0.95 * n:
            log.info("%s: %d clips already", name, have)
            resample_dir(d)  # an interrupted run may have left some at the TTS rate
            continue
        todo = n - have
        log.info("%s: generating %d", name, todo)
        done = 0
        for model, share in gens:
            gen_pt(str(V / model), texts[lang_of(model)], d, int(todo * share), a.batch, noise)
            done += int(todo * share)
        gen_onnx(train_onnx, lambda m: texts[lang_of(m)], d, todo - done, noise)
        resample_dir(d)
        log.info("%s: %d clips", name, count(d))

    # Held out: voices training never saw.
    if count(dirs["heldout_positive"]) < 500:
        gen_onnx(test_onnx, lambda m: W[lang_of(m)], dirs["heldout_positive"], 1200, 0.667)
        if a.edge:
            asyncio.run(_edge(W, dirs["heldout_positive"], ["-15%", "+0%", "+15%"], ["-5Hz", "+0Hz", "+5Hz"]))
        resample_dir(dirs["heldout_positive"])
    if count(dirs["heldout_negative"]) < 500:
        heldneg = {lang: EN_ALONE + [t for w in other_word(a.word) for t in WORDS[w][lang]] for lang in W}
        heldneg["nl"] = NL_ALONE + [t for w in other_word(a.word) for t in WORDS[w]["nl"]] + nl_neg[:200]
        gen_onnx(test_onnx, lambda m: heldneg[lang_of(m)], dirs["heldout_negative"], 1800, 0.667)
        if a.edge:
            few = {k: random.sample(v, min(40, len(v))) for k, v in heldneg.items()}
            asyncio.run(_edge(few, dirs["heldout_negative"], ["+0%"], ["+0Hz"]))
        resample_dir(dirs["heldout_negative"])
    for k, d in dirs.items():
        log.info("%-18s %6d", k, count(d))


if __name__ == "__main__":
    main()
