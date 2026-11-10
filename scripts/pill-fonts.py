"""Make the pill's typefaces: internal/pill/font/<font>-<weight>.ttf.

The page uses variable fonts (web/fonts-*.woff2); the pill draws with
golang.org/x/image, which reads TrueType but neither WOFF2 nor variable axes.
So each ui.font choice is cut here into three static weights — regular (400),
semibold (600) and bold (700) — subset to the scripts the page's files cover
(Latin with its extensions, Cyrillic, Greek, Vietnamese) and without hinting.

The sources are google/fonts at a pinned commit (all OFL). Needs fontTools:
    pip install fonttools
    python scripts/pill-fonts.py
"""
import io, os, urllib.request
from fontTools.ttLib import TTFont
from fontTools.varLib.instancer import instantiateVariableFont
from fontTools import subset

COMMIT = "5bb28733cec313b63fbd415b574b5ae35c8aa8b8"
# ui.font -> google/fonts path
FONTS = {
    "inter": "ofl/inter/Inter[opsz,wght].ttf",
    "atkinson": "ofl/atkinsonhyperlegiblenext/AtkinsonHyperlegibleNext[wght].ttf",
    "noto-sans": "ofl/notosans/NotoSans[wdth,wght].ttf",
    "nunito": "ofl/nunito/Nunito[wght].ttf",
    "literata": "ofl/literata/Literata[opsz,wght].ttf",
    "jetbrains-mono": "ofl/jetbrainsmono/JetBrainsMono[wght].ttf",
    "noto-mono": "ofl/notosansmono/NotoSansMono[wdth,wght].ttf",
}
WEIGHTS = {"regular": 400, "semibold": 600, "bold": 700}
# The unicode-ranges of the page's latin, latin-ext, cyrillic(-ext), greek(-ext)
# and vietnamese files, plus the punctuation and symbols in between.
RANGES = [(0x0000, 0x024F), (0x0259, 0x0259), (0x02B0, 0x02FF), (0x0300, 0x036F), (0x0370, 0x03FF),
          (0x0400, 0x052F), (0x1C80, 0x1C8F), (0x1D00, 0x1DBF), (0x1E00, 0x1FFF), (0x2000, 0x20CF),
          (0x2100, 0x218F), (0x2190, 0x21FF), (0x2200, 0x22FF), (0x2C60, 0x2C7F), (0x2DE0, 0x2DFF),
          (0xA640, 0xA69F), (0xA720, 0xA7FF), (0xFB00, 0xFB06), (0xFE2E, 0xFE2F), (0xFEFF, 0xFEFF), (0xFFFD, 0xFFFD)]

out = os.path.join(os.path.dirname(__file__), "..", "internal", "pill", "font")
unicodes = [u for a, b in RANGES for u in range(a, b + 1)]
for name, path in FONTS.items():
    url = f"https://raw.githubusercontent.com/google/fonts/{COMMIT}/{urllib.request.quote(path)}"
    data = urllib.request.urlopen(url, timeout=120).read()
    for wname, w in WEIGHTS.items():
        f = TTFont(io.BytesIO(data))
        axes = {a.axisTag: a for a in f["fvar"].axes}
        loc = {}
        for tag, a in axes.items():
            if tag == "wght":
                loc[tag] = max(a.minValue, min(a.maxValue, w))
            elif tag == "wdth":
                loc[tag] = 100
            else:
                loc[tag] = a.defaultValue  # opsz and the like
        f = instantiateVariableFont(f, loc, updateFontNames=False)
        opt = subset.Options()
        opt.hinting = False
        opt.desubroutinize = True
        opt.layout_features = ["kern", "liga", "calt", "ccmp", "locl", "mark", "mkmk"]
        opt.name_IDs = ["*"]
        opt.notdef_outline = True
        s = subset.Subsetter(opt)
        s.populate(unicodes=unicodes)
        s.subset(f)
        dst = os.path.join(out, f"{name}-{wname}.ttf")
        f.save(dst)
        print(dst, os.path.getsize(dst))
