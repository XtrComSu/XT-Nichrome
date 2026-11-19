#!/usr/bin/env bash
# Downloads freely licensed, metrically compatible fonts so the Chromium persona
# has Windows-like text metrics and font availability. No system install needed:
# the daemon points Chromium's fontconfig at this directory tree.
#
#   Carlito   (OFL) -> metric-compatible with Calibri
#   Caladea   (OFL) -> metric-compatible with Cambria
#   Gelasio   (OFL) -> metric-compatible with Georgia
#   Selawik   (OFL) -> metric-compatible with Segoe UI
#   Liberation (OFL) -> metric-compatible with Arial / Times New Roman / Courier New
#   Tinos     (OFL) -> Times New Roman metrics (fallback)
set -euo pipefail
ROOT="${XT_FONT_ROOT:-$HOME/.local/share/xt-nichrome/fonts}"
DEST="$ROOT/windows11"
mkdir -p "$DEST"
cd "$DEST"

get() { # get <url> <outfile>
  local url="$1" out="$2"
  if [ -s "$out" ]; then echo "have $out"; return 0; fi
  echo "fetch $out"
  curl -fsSL --retry 3 --max-time 90 -o "$out.tmp" "$url"
  mv "$out.tmp" "$out"
}

GF=https://raw.githubusercontent.com/google/fonts/main/ofl
get "$GF/carlito/Carlito-Regular.ttf" Carlito-Regular.ttf
get "$GF/carlito/Carlito-Bold.ttf" Carlito-Bold.ttf
get "$GF/carlito/Carlito-Italic.ttf" Carlito-Italic.ttf
get "$GF/carlito/Carlito-BoldItalic.ttf" Carlito-BoldItalic.ttf
get "$GF/caladea/Caladea-Regular.ttf" Caladea-Regular.ttf
get "$GF/caladea/Caladea-Bold.ttf" Caladea-Bold.ttf
get "$GF/gelasio/Gelasio%5Bwght%5D.ttf" Gelasio.ttf || true
get "https://github.com/microsoft/Selawik/raw/master/fonts/selawk.ttf" selawk.ttf || true
get "https://github.com/microsoft/Selawik/raw/master/fonts/selawkb.ttf" selawkb.ttf || true
get "https://github.com/liberationfonts/liberation-fonts/raw/main/src/LiberationSans-Regular.ttf" LiberationSans-Regular.ttf || true

# Liberation fonts ship as a release zip on some mirrors; fall back to Debian's copy.
if [ ! -s LiberationSans-Regular.ttf ]; then
  get "https://github.com/liberationfonts/liberation-fonts/files/7261482/liberation-fonts-ttf-2.1.5.tar.gz" lib.tar.gz || true
  if [ -s lib.tar.gz ]; then tar xzf lib.tar.gz --strip-components=1 2>/dev/null || true; fi
fi
ls -1
