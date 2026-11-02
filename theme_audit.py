#!/usr/bin/env python3
"""Audit and repair the dashboard's theme palettes.

Rules enforced per theme:
  text  vs bg  >= 7.0     (body copy)
  head  vs bg  >= 9.0     (headings, capped by what the hue allows)
  muted vs bg  >= 4.5
  faint vs bg  >= 3.0
  accent  vs bg >= 4.5    (must be readable as data/controls)
  accent2 vs bg >= 3.0
  every entry in accents[] vs bg >= 4.5, and pairwise delta-E >= 22 so no two
  dots in the picker read as the same colour.

Where a value fails, lightness is moved (hue and saturation preserved) until it
passes; where two accents are too close, the later one's hue is rotated until it
separates. Colours that already pass are never touched, so the themes keep their
character.
"""
import colorsys
import json
import math
import re
import sys

import os

# resolve next to the script so a fresh checkout works; an argument overrides
PATH = sys.argv[1] if len(sys.argv) > 1 else os.path.join(
    os.path.dirname(os.path.abspath(__file__)), "index.html")


def hex_to_rgb(h):
    h = h.lstrip("#")
    return tuple(int(h[i:i + 2], 16) / 255 for i in (0, 2, 4))


def rgb_to_hex(rgb):
    return "#" + "".join(f"{max(0, min(255, round(c * 255))):02x}" for c in rgb)


def lum(h):
    r, g, b = hex_to_rgb(h)
    f = lambda c: c / 12.92 if c <= 0.03928 else ((c + 0.055) / 1.055) ** 2.4
    return 0.2126 * f(r) + 0.7152 * f(g) + 0.0722 * f(b)


def contrast(a, b):
    la, lb = sorted([lum(a), lum(b)], reverse=True)
    return (la + 0.05) / (lb + 0.05)


def to_lab(h):
    r, g, b = hex_to_rgb(h)
    f = lambda c: ((c + 0.055) / 1.055) ** 2.4 if c > 0.04045 else c / 12.92
    r, g, b = f(r), f(g), f(b)
    x = (r * 0.4124 + g * 0.3576 + b * 0.1805) / 0.95047
    y = (r * 0.2126 + g * 0.7152 + b * 0.0722) / 1.00000
    z = (r * 0.0193 + g * 0.1192 + b * 0.9505) / 1.08883
    f2 = lambda t: t ** (1 / 3) if t > 0.008856 else 7.787 * t + 16 / 116
    fx, fy, fz = f2(x), f2(y), f2(z)
    return (116 * fy - 16, 500 * (fx - fy), 200 * (fy - fz))


def delta_e(a, b):
    la, lb = to_lab(a), to_lab(b)
    return math.sqrt(sum((x - y) ** 2 for x, y in zip(la, lb)))


def adjust(hex_colour, bg, target):
    """Move lightness only, in HSL, until contrast(colour, bg) >= target."""
    if contrast(hex_colour, bg) >= target:
        return hex_colour, False
    r, g, b = hex_to_rgb(hex_colour)
    h, l, s = colorsys.rgb_to_hls(r, g, b)
    best = hex_colour
    for step in range(1, 101):
        cand_l = min(0.92, l + step * 0.008)
        cand = rgb_to_hex(colorsys.hls_to_rgb(h, cand_l, s))
        best = cand
        if contrast(cand, bg) >= target:
            return cand, True
        if cand_l >= 0.92:
            break
    return best, True


def separate(hex_colour, others, bg, min_de=22, target_contrast=4.5):
    """Rotate hue until this colour is distinguishable from the others."""
    if all(delta_e(hex_colour, o) >= min_de for o in others):
        return hex_colour, False
    r, g, b = hex_to_rgb(hex_colour)
    h, l, s = colorsys.rgb_to_hls(r, g, b)
    for shift in range(10, 200, 10):
        for direction in (1, -1):
            cand = rgb_to_hex(colorsys.hls_to_rgb((h + direction * shift / 360) % 1.0, l, s))
            cand, _ = adjust(cand, bg, target_contrast)
            if all(delta_e(cand, o) >= min_de for o in others):
                return cand, True
    return hex_colour, False


def main():
    src = open(PATH).read()
    m = re.search(r"const TOKENS = (\{.*?\});\n", src, re.S)
    if m is None:
        sys.exit(f"{PATH}: no TOKENS declaration found")
    tokens = json.loads(m.group(1))

    changes = []
    for name, t in tokens.items():
        bg = t["bg"]
        for field, target in (("text", 7.0), ("head", 9.0), ("muted", 4.5), ("faint", 3.0),
                              ("accent", 4.5), ("accent2", 3.0), ("accent3", 4.0), ("line", 1.6)):
            if field in t and t[field].startswith("#"):
                new, changed = adjust(t[field], bg, target)
                if changed:
                    changes.append(f"{name}.{field}: {t[field]} -> {new} (contrast {contrast(t[field],bg):.2f} -> {contrast(new,bg):.2f})")
                    t[field] = new

        # the accent dots: readable on the ground and distinct from each other
        if "accents" in t:
            fixed = []
            for c in t["accents"]:
                c2, _ = adjust(c, bg, 4.5)
                c3, _ = separate(c2, fixed, bg)
                if c3 != c:
                    changes.append(f"{name}.accents: {c} -> {c3}")
                fixed.append(c3)
            t["accents"] = fixed

    out = src[: m.start(1)] + json.dumps(tokens, ensure_ascii=False) + src[m.end(1) :]
    open(PATH, "w").write(out)

    print(f"{len(changes)} adjustments\n")
    for c in changes:
        print(" ", c)

    print("\nfinal contrast table")
    print(f"{'theme':11} {'accent/bg':>10} {'acc2/bg':>8} {'text/bg':>8} {'min dot dE':>11}")
    for name, t in tokens.items():
        dots = t.get("accents", [])
        min_de = min((delta_e(a, b) for i, a in enumerate(dots) for b in dots[i + 1:]), default=0)
        print(f"{name:11} {contrast(t['accent'], t['bg']):>10.2f} {contrast(t.get('accent2', t['bg']), t['bg']):>8.2f} "
              f"{contrast(t['text'], t['bg']):>8.2f} {min_de:>11.1f}")


if __name__ == "__main__":
    main()
