"""The contour-lines background (--topo in web/index.html): contours of a
periodic height field as an SVG tile that repeats seamlessly, URL-encoded.
    python scripts/backdrop-contours.py out.txt
"""
import math, random, sys

W = H = 640
N = 128  # grid cells per side
r = random.Random(5)
waves = []
for _ in range(9):
    kx, ky = r.randint(-3, 3), r.randint(-3, 3)
    if kx == 0 and ky == 0:
        continue
    amp = 1 / math.hypot(kx, ky) ** 1.2
    waves.append((kx, ky, amp, r.uniform(0, 2 * math.pi)))

def f(i, j):  # periodic over N
    x, y = i / N, j / N
    return sum(a * math.sin(2 * math.pi * (kx * x + ky * y) + p) for kx, ky, a, p in waves)

g = [[f(i, j) for i in range(N + 1)] for j in range(N + 1)]
lo = min(min(row) for row in g); hi = max(max(row) for row in g)
levels = [lo + (hi - lo) * (k + 0.5) / 14 for k in range(14)]
s = W / N

def segs(level):
    out = []
    for j in range(N):
        for i in range(N):
            v = [g[j][i], g[j][i + 1], g[j + 1][i + 1], g[j + 1][i]]
            c = [(i, j), (i + 1, j), (i + 1, j + 1), (i, j + 1)]
            pts = []
            for e in range(4):
                a, b = v[e], v[(e + 1) % 4]
                if (a < level) != (b < level):
                    t = (level - a) / (b - a)
                    (x0, y0), (x1, y1) = c[e], c[(e + 1) % 4]
                    pts.append((round((x0 + t * (x1 - x0)) * s, 1), round((y0 + t * (y1 - y0)) * s, 1)))
            if len(pts) == 2:
                out.append(tuple(pts))
            elif len(pts) == 4:
                out.append((pts[0], pts[1])); out.append((pts[2], pts[3]))
    return out

def chains(ss):
    # join segments sharing end points into polylines
    adj = {}
    for a, b in ss:
        adj.setdefault(a, []).append(b); adj.setdefault(b, []).append(a)
    seen = set(); lines = []
    def key(a, b): return (a, b) if a < b else (b, a)
    for a, b in ss:
        if key(a, b) in seen: continue
        seen.add(key(a, b)); line = [a, b]
        for end in (0, 1):
            while True:
                p = line[-1] if end else line[0]
                nxt = [q for q in adj[p] if key(p, q) not in seen]
                if not nxt: break
                q = nxt[0]; seen.add(key(p, q))
                if end: line.append(q)
                else: line.insert(0, q)
        lines.append(line)
    return lines

def simplify(pts, eps=0.6):
    if len(pts) < 3: return pts
    (x0, y0), (x1, y1) = pts[0], pts[-1]
    dx, dy = x1 - x0, y1 - y0; L = math.hypot(dx, dy) or 1e-9
    d = [abs(dy * (x - x0) - dx * (y - y0)) / L for x, y in pts[1:-1]]
    i = max(range(len(d)), key=d.__getitem__)
    if d[i] > eps:
        return simplify(pts[:i + 2], eps)[:-1] + simplify(pts[i + 1:], eps)
    return [pts[0], pts[-1]]

paths = []
for k, lv in enumerate(levels):
    for line in chains(segs(lv)):
        p = simplify(line)
        d = "M" + " L".join(f"{x:g} {y:g}" for x, y in p)
        paths.append(d)
w = 1.1
svg = (f"<svg xmlns='http://www.w3.org/2000/svg' width='{W}' height='{H}'>"
       f"<path fill='none' stroke='#000' stroke-width='{w}' stroke-linejoin='round' d='{' '.join(paths)}'/></svg>")
enc = svg.replace("<", "%3C").replace(">", "%3E").replace("#", "%23")
open(sys.argv[1], "w").write(enc)
print(len(enc))
