#!/usr/bin/env python3
"""Build a Windows-11-shaped font surface from freely licensed faces.

The point is that font *probing* (canvas metrics, document.fonts, Local Font
Access) must return the same answer a stock Windows 11 Chrome returns. Faking
that in JavaScript is fragile and gets detected; instead we rename
metric-compatible OFL fonts to the Windows family names and point Chromium's
fontconfig at ONLY this directory, so the Linux desktop font set (Adwaita,
Geist, JetBrains, Nerd Fonts, ~880 families) cannot leak into a probe.

Sources:
  Carlito   -> Calibri      (metric-compatible, OFL)
  Caladea   -> Cambria      (metric-compatible, OFL)
  Gelasio   -> Georgia      (metric-compatible, OFL)
  Liberation Sans  -> Arial / Segoe UI / Tahoma / Verdana
  Liberation Serif -> Times New Roman / Palatino Linotype / Book Antiqua
  Liberation Mono  -> Courier New / Consolas / Lucida Console
  FreeSans / FreeSerif / FreeMono -> symbol + legacy faces
  Noto Color Emoji -> Segoe UI Emoji
  Noto Sans CJK    -> Microsoft YaHei / MS Gothic / SimSun / Malgun Gothic
"""
import os
import sys
from fontTools.ttLib import TTFont

BASE = os.path.expanduser("~/.local/share/xt-nichrome/fonts")
SRC_DIRS = [os.path.join(BASE, "windows11"), os.path.join(BASE, "extra")]
OUT = os.path.join(BASE, "w11")

SYS = {
    "NotoColorEmoji.ttf": "/usr/share/fonts/noto/NotoColorEmoji.ttf",
    "NotoSansCJKsc": "/usr/share/fonts/noto-cjk/NotoSansCJK-Regular.ttc",
    "FreeSans.otf": "/usr/share/fonts/gnu-free/FreeSans.otf",
    "FreeSerif.otf": "/usr/share/fonts/gnu-free/FreeSerif.otf",
    "FreeMono.otf": "/usr/share/fonts/gnu-free/FreeMono.otf",
}

# (windows family, style, source file, source face index)
MAP = [
    # Core UI / text faces.
    ("Segoe UI", "Regular", "Carlito-Regular.ttf", 0),
    ("Segoe UI", "Bold", "Carlito-Bold.ttf", 0),
    ("Segoe UI", "Italic", "Carlito-Italic.ttf", 0),
    ("Segoe UI", "Bold Italic", "Carlito-BoldItalic.ttf", 0),
    ("Calibri", "Regular", "Carlito-Regular.ttf", 0),
    ("Calibri", "Bold", "Carlito-Bold.ttf", 0),
    ("Calibri", "Italic", "Carlito-Italic.ttf", 0),
    ("Calibri", "Bold Italic", "Carlito-BoldItalic.ttf", 0),
    ("Cambria", "Regular", "Caladea-Regular.ttf", 0),
    ("Cambria", "Bold", "Caladea-Bold.ttf", 0),
    ("Georgia", "Regular", "Gelasio.ttf", 0),
    ("Arial", "Regular", "LiberationSans-Regular.ttf", 0),
    ("Arial", "Bold", "LiberationSans-Bold.ttf", 0),
    ("Arial", "Italic", "LiberationSans-Italic.ttf", 0),
    ("Arial", "Bold Italic", "LiberationSans-BoldItalic.ttf", 0),
    ("Arial Black", "Regular", "LiberationSans-Bold.ttf", 0),
    ("Helvetica", "Regular", "LiberationSans-Regular.ttf", 0),
    ("Tahoma", "Regular", "LiberationSans-Regular.ttf", 0),
    ("Tahoma", "Bold", "LiberationSans-Bold.ttf", 0),
    ("Verdana", "Regular", "LiberationSans-Regular.ttf", 0),
    ("Verdana", "Bold", "LiberationSans-Bold.ttf", 0),
    ("Trebuchet MS", "Regular", "LiberationSans-Regular.ttf", 0),
    ("Trebuchet MS", "Bold", "LiberationSans-Bold.ttf", 0),
    ("Comic Sans MS", "Regular", "Carlito-Regular.ttf", 0),
    ("Franklin Gothic Medium", "Regular", "LiberationSans-Regular.ttf", 0),
    ("Impact", "Regular", "LiberationSans-Bold.ttf", 0),
    ("Lucida Sans Unicode", "Regular", "LiberationSans-Regular.ttf", 0),
    ("Microsoft Sans Serif", "Regular", "LiberationSans-Regular.ttf", 0),
    ("Palatino Linotype", "Regular", "LiberationSerif-Regular.ttf", 0),
    ("Book Antiqua", "Regular", "LiberationSerif-Regular.ttf", 0),
    ("Times New Roman", "Regular", "LiberationSerif-Regular.ttf", 0),
    ("Times New Roman", "Bold", "LiberationSerif-Bold.ttf", 0),
    ("Times New Roman", "Italic", "LiberationSerif-Italic.ttf", 0),
    ("Times New Roman", "Bold Italic", "LiberationSerif-BoldItalic.ttf", 0),
    ("Courier New", "Regular", "LiberationMono-Regular.ttf", 0),
    ("Courier New", "Bold", "LiberationMono-Bold.ttf", 0),
    ("Courier New", "Italic", "LiberationMono-Italic.ttf", 0),
    ("Courier New", "Bold Italic", "LiberationMono-BoldItalic.ttf", 0),
    ("Consolas", "Regular", "LiberationMono-Regular.ttf", 0),
    ("Consolas", "Bold", "LiberationMono-Bold.ttf", 0),
    ("Lucida Console", "Regular", "LiberationMono-Regular.ttf", 0),
    ("Candara", "Regular", "Carlito-Regular.ttf", 0),
    ("Constantia", "Regular", "Caladea-Regular.ttf", 0),
    ("Corbel", "Regular", "Carlito-Regular.ttf", 0),
    ("Gabriola", "Regular", "Caladea-Regular.ttf", 0),
    ("Sylfaen", "Regular", "Caladea-Regular.ttf", 0),
    ("MV Boli", "Regular", "LiberationSans-Regular.ttf", 0),
    ("Nirmala UI", "Regular", "LiberationSans-Regular.ttf", 0),
    ("Bahnschrift", "Regular", "LiberationSans-Regular.ttf", 0),
    ("Ebrima", "Regular", "FreeSans.otf", 0),
    ("Gadugi", "Regular", "FreeSans.otf", 0),
    ("Javanese Text", "Regular", "FreeSans.otf", 0),
    ("Leelawadee UI", "Regular", "FreeSans.otf", 0),
    ("Malgun Gothic", "Regular", "NotoSansCJKsc", 0),
    ("Microsoft Himalaya", "Regular", "FreeSerif.otf", 0),
    ("Microsoft JhengHei", "Regular", "NotoSansCJKsc", 0),
    ("Microsoft New Tai Lue", "Regular", "FreeSans.otf", 0),
    ("Microsoft PhagsPa", "Regular", "FreeSerif.otf", 0),
    ("Microsoft Tai Le", "Regular", "FreeSerif.otf", 0),
    ("Microsoft YaHei", "Regular", "NotoSansCJKsc", 0),
    ("Microsoft YaHei", "Bold", "NotoSansCJKsc", 0),
    ("MingLiU-ExtB", "Regular", "NotoSansCJKsc", 0),
    ("Mongolian Baiti", "Regular", "FreeSerif.otf", 0),
    ("MS Gothic", "Regular", "NotoSansCJKsc", 0),
    ("MS PGothic", "Regular", "NotoSansCJKsc", 0),
    ("Myanmar Text", "Regular", "FreeSerif.otf", 0),
    ("SimSun", "Regular", "NotoSansCJKsc", 0),
    ("SimSun-ExtB", "Regular", "NotoSansCJKsc", 0),
    ("Sitka", "Regular", "Caladea-Regular.ttf", 0),
    ("Segoe MDL2 Assets", "Regular", "FreeSans.otf", 0),
    ("Segoe Print", "Regular", "FreeSerif.otf", 0),
    ("Segoe Script", "Regular", "FreeSerif.otf", 0),
    ("Segoe UI Emoji", "Regular", "NotoColorEmoji.ttf", 0),
    ("Segoe UI Historic", "Regular", "FreeSans.otf", 0),
    ("Segoe UI Symbol", "Regular", "FreeSans.otf", 0),
    ("Symbol", "Regular", "FreeSerif.otf", 0),
    ("Webdings", "Regular", "FreeSans.otf", 0),
    ("Wingdings", "Regular", "FreeSans.otf", 0),
    ("Yu Gothic", "Regular", "NotoSansCJKsc", 0),
]


def find_source(name):
    for d in SRC_DIRS:
        p = os.path.join(d, name)
        if os.path.exists(p) and os.path.getsize(p) > 1000:
            return p
    if name in SYS and os.path.exists(SYS[name]):
        return SYS[name]
    return None


def open_face(path, index):
    f = TTFont(path, fontNumber=index) if path.lower().endswith(".ttc") else TTFont(path)
    return f


def rename(f, family, style, out):
    name = f["name"]
    full = family if style == "Regular" else f"{family} {style}"
    ps = (family + "-" + style).replace(" ", "")
    for rec in list(name.names):
        if rec.nameID == 1:
            new = family
        elif rec.nameID == 2:
            new = style
        elif rec.nameID == 4:
            new = full
        elif rec.nameID == 6:
            new = ps
        elif rec.nameID in (16, 21):
            new = family
        elif rec.nameID in (17, 22):
            new = style
        else:
            continue
        try:
            name.setName(new, rec.nameID, rec.platformID, rec.platEncID, rec.langID)
        except Exception:
            pass
    f.save(out)


def main():
    os.makedirs(OUT, exist_ok=True)
    built, missing = 0, []
    for family, style, src, idx in MAP:
        path = find_source(src)
        if not path:
            missing.append(src)
            continue
        out = os.path.join(OUT, (family + "-" + style).replace(" ", "") + (".otf" if src.endswith(".otf") else ".ttf"))
        if os.path.exists(out):
            built += 1
            continue
        try:
            f = open_face(path, idx)
            rename(f, family, style, out)
            built += 1
        except Exception as e:
            print(f"FAIL {family} {style} <- {src}: {e}", file=sys.stderr)
    print(f"built/kept {built} faces in {OUT}")
    if missing:
        print("missing sources:", sorted(set(missing)))


if __name__ == "__main__":
    main()
