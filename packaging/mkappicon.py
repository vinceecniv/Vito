#!/usr/bin/env python3
"""Generate the app icon, web/icon-512.png and web/icon-192.png, from the mark
in mkwizard.py (the five bars of web/favicon.svg and the page's MARK).

Everything else follows from these two files: packaging/vito.ico
(go run ./packaging/mkicon), the macOS .icns and the Linux icons (built from
icon-512.png at packaging time), and the installed web app.

Run from the repo root:  python packaging/mkappicon.py
"""
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from mkwizard import ROOT, mark  # noqa: E402

for size in (512, 192):
    mark(size).save(os.path.join(ROOT, "web", f"icon-{size}.png"), optimize=True)
    print(f"web/icon-{size}.png")
