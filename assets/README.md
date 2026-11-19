# XT-Nichrome Brand & Logo Assets (`Ni` — Element 28)

XT-Nichrome uses the XT industrial geometric house style paired with the periodic-table element tile for **Nickel (`Ni`, atomic number `28`, atomic mass `58.6934`)** — the primary constituent of the nichrome resistance alloy (`80% Ni / 20% Cr`).

All SVG files in this directory are **100% pure vector `<path>` geometry** with zero `<text>` or external font dependencies, guaranteeing identical rendering across Linux, Windows, macOS, GitHub, and headless rasterizers.

## Color Palette

| Token | Hex | Usage |
| --- | --- | --- |
| **Obsidian Tile** | `#0A0A0A` | Primary dark tile fill and canvas background |
| **Graphite Hairline** | `#262626` | Chamfered periodic-tile border and divider rule |
| **Alloy White** | `#E8E6E3` | Primary geometric letterforms (`Ni`, `X`, `NICKEL`, `-NICHROME`) |
| **Crimson Accent** | `#FF0000` / `#FF2E2E` | Atomic number `28`, `T` glyph, `i` tittle, and top-right chamfer bar |
| **Muted Zinc** | `#A1A1AA` | Atomic mass `58.6934` and technical sub-caption |
| **Light Paper (Light Variant)** | `#F5F4F0` | Tile background for `logo-ni-light.svg` |

## Construction Grid

- **Periodic Tile (`logo-ni*.svg`):** `512 × 512` viewBox with `24 px` / `64 px` asymmetric industrial corner chamfers (`45°` bevels), `12 px` inset hairline frame, and `36 px` primary stem weight on the central `Ni` symbol.
- **Display Letterforms (`logo-word.svg`):** Constructed on a `60 × 100` unit cell with `16` unit stem/crossbar thickness and `20` unit inter-glyph tracking, paired with the authentic XT chevron (`X`) and crimson `T`.
- **Favicon (`favicon.svg`):** `32 × 32` hexagonal perimeter ring (`1.8 px` stroke) housing a high-contrast `Ni` monogram with a crimson `2.4 × 2.4` tittle for sub-32px legibility.

## Clear Space & Minimum Sizes

- **Clear Space:** Maintain at least `1/8` of the tile width (`64 px` at `512 × 512`) around all sides of `logo-ni.svg` and `logo-mark.svg`.
- **Minimum Sizes:**
  - `logo-ni.svg` (full periodic tile with `NICKEL` and `58.6934`): `>= 64 × 64 px`
  - `logo-mark.svg` (simplified `28 / Ni / XT` badge): `24 × 24 px` to `64 × 64 px`
  - `favicon.svg` (hex monogram): `16 × 16 px` to `32 × 32 px`

## Asset Inventory

| Vector SVG | Raster PNG Exports (`assets/png/`) | Description |
| --- | --- | --- |
| `logo-ni.svg` | `logo-ni-512.png`, `logo-ni-128.png` | Primary dark chamfered `Ni` periodic-table tile |
| `logo-ni-dark.svg` | `logo-ni-dark-512.png`, `logo-ni-dark-128.png` | Transparent-background variant for dark surfaces |
| `logo-ni-light.svg` | `logo-ni-light-512.png`, `logo-ni-light-128.png` | High-contrast light tile (`#F5F4F0`) for light surfaces |
| `logo-ni-monochrome.svg` | `logo-ni-monochrome-512.png`, `logo-ni-monochrome-128.png` | Single-ink (`#E8E6E3`) stencil/emboss variant |
| `logo-mark.svg` | `logo-mark-512.png`, `logo-mark-128.png` | Rounded-square XT app icon with `Ni-28` mark |
| `favicon.svg` | `favicon-128.png`, `favicon-32.png` | `32 × 32` hexagonal favicon mark |
| `logo-word.svg` | `logo-word-1024.png`, `social-preview.png` (`1280×640`) | Full horizontal banner lockup (`Ni` tile + `XT-NICHROME`) |
