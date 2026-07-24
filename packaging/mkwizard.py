#!/usr/bin/env python3
"""Generate the branded Inno Setup wizard images for the Windows installer.

Produces packaging/wizard-large.bmp (the tall panel on the Welcome/Finished
pages) and packaging/wizard-small.bmp (the mark shown top-right on inner pages),
both in Vito's house style: the coral→violet gradient, the rounded-square mark
with its five equaliser bars, and the Baloo 2 / Sora wordmark and tagline.

Run from the repo root:  python3 packaging/mkwizard.py
Needs: pip install pillow fonttools ; the brand fonts in web/*.woff2.
Re-run whenever the branding changes; commit the resulting .bmp files.
"""
import os
from PIL import Image, ImageDraw, ImageFont

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
CORAL = (0xFF, 0x6B, 0x5E)
VIOLET = (0x7C, 0x3A, 0xED)
FONTS = os.environ.get("VITO_FONTS", "/tmp/claude-1000/-home-vincent-Code-Vito/228b0e4f-0887-483c-8204-a49c3dbf24c1/scratchpad/fonts")


def lerp(a, b, t):
    return tuple(round(a[i] + (b[i] - a[i]) * t) for i in range(3))


def diagonal_gradient(w, h, c0, c1):
    """Corner-to-corner gradient (top-left c0 → bottom-right c1), like the app's
    135° brand gradient."""
    img = Image.new("RGB", (w, h))
    px = img.load()
    denom = (w - 1) + (h - 1)
    for y in range(h):
        for x in range(w):
            px[x, y] = lerp(c0, c1, (x + y) / denom)
    return img


def rounded_mask(w, h, r):
    m = Image.new("L", (w, h), 0)
    ImageDraw.Draw(m).rounded_rectangle([0, 0, w - 1, h - 1], radius=r, fill=255)
    return m


def mark(size, bg="gradient"):
    """The Vito mark: a rounded square (coral→violet) with five white equaliser
    bars, matching web/favicon.svg. Returned RGBA so it can be composited."""
    s = size * 4  # supersample for crisp edges, then downscale
    tile = diagonal_gradient(s, s, CORAL, VIOLET) if bg == "gradient" else Image.new("RGB", (s, s), bg)
    tile = tile.convert("RGBA")
    # bars: x, height (fractions of the 100-unit viewBox in the SVG, scaled 0.72)
    d = ImageDraw.Draw(tile)
    # SVG: translate(14,14) scale(0.72); bars at x=4,24,44,64,84 width 12, y=22,
    # heights 20,36,56,36,20 (rx 6). Reproduce in the s×s tile.
    def u(v):  # SVG unit → pixel in this tile
        return (14 + v * 0.72) / 100 * s
    bars = [(4, 20), (24, 36), (44, 56), (64, 36), (84, 20)]
    for bx, bh in bars:
        x0 = u(bx); x1 = u(bx + 12); y0 = u(22); y1 = u(22 + bh)
        rad = (x1 - x0) / 2
        d.rounded_rectangle([x0, y0, x1, y1], radius=rad, fill=(255, 255, 255, 255))
    tile.putalpha(rounded_mask(s, s, int(s * 0.24)))
    return tile.resize((size, size), Image.LANCZOS)


def font(name, px):
    return ImageFont.truetype(os.path.join(FONTS, name), px)


def make_large(path):
    W, H = 410, 797  # Inno's largest default WizardImage size
    img = diagonal_gradient(W, H, CORAL, VIOLET)
    d = ImageDraw.Draw(img, "RGBA")

    # Faint equaliser motif along the bottom, as texture.
    heights = [40, 90, 150, 220, 300, 210, 130, 70, 120, 200, 260, 160, 80, 40]
    bw, gap = 16, 12
    total = len(heights) * bw + (len(heights) - 1) * gap
    x = (W - total) / 2
    for hgt in heights:
        d.rounded_rectangle([x, H - 60 - hgt, x + bw, H - 60], radius=bw / 2,
                            fill=(255, 255, 255, 28))
        x += bw + gap

    m = mark(150)
    img.paste(m, ((W - 150) // 2, 150), m)

    wm = font("baloo2.ttf", 92)
    tag = font("sora.ttf", 26)
    def center(text, f, y, fill):
        tw = d.textbbox((0, 0), text, font=f)[2]
        d.text(((W - tw) / 2, y), text, font=f, fill=fill)
    center("Vito", wm, 322, (255, 255, 255, 255))
    center("Voice In, Text Out", tag, 440, (255, 255, 255, 235))

    img.save(path)  # 24-bit BMP
    return (W, H)


def make_small(path):
    W, H = 138, 140  # Inno's largest default WizardSmallImage size
    img = Image.new("RGB", (W, H), (255, 255, 255))  # white, to blend with the page
    m = mark(118)
    img.paste(m, ((W - 118) // 2, (H - 118) // 2), m)
    img.save(path)
    return (W, H)


if __name__ == "__main__":
    lg = make_large(os.path.join(ROOT, "packaging", "wizard-large.bmp"))
    sm = make_small(os.path.join(ROOT, "packaging", "wizard-small.bmp"))
    # PNG previews (not shipped) so the design can be eyeballed without Windows.
    prev = os.environ.get("VITO_PREVIEW")
    if prev:
        Image.open(os.path.join(ROOT, "packaging", "wizard-large.bmp")).save(os.path.join(prev, "wizard-large.png"))
        Image.open(os.path.join(ROOT, "packaging", "wizard-small.bmp")).save(os.path.join(prev, "wizard-small.png"))
    print(f"wizard-large.bmp {lg}  wizard-small.bmp {sm}")
