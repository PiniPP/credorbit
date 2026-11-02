# CredOrbit brand

> Your Cloud Access. In Orbit.

## Concept: Orbit Ring + Identity Core

CredOrbit keeps every cloud account and role you have in a stable, synchronised
orbit around a single identity. The mark says exactly that:

- **Identity core.** A cyan node at the centre with a soft halo: you, signed in once.
- **Orbit ring.** A tilted elliptical orbit in Orbit Blue, open on the right so
  it reads as a subtle letter **C**.
- **Orbital nodes.** Three nodes on the ring stand for accounts and roles. The
  leading node, in cyan, sits where the ring opens.
- **Motion.** A cyan arc runs along the ring into the leading node, with a fine
  trail just outside it. It suggests synchronisation and continuous refresh,
  not a lock or a key.

The mark is built from plain geometry: one ellipse, three circles, two arcs. It
contains no padlocks, keys, clouds or icon-library glyphs.

## Files

| File | Use |
|------|-----|
| `logo-symbol.svg` | Primary symbol, transparent. Works on dark and light backgrounds. |
| `logo-symbol-dark.svg` | Symbol on a `#0B1020` rounded square (avatars, favicons). |
| `logo-symbol-light.svg` | Deepened colours for white/light backgrounds. |
| `logo-mono.svg` | One colour (`currentColor`): docs, print, embossing, terminals. |
| `app-icon.svg` | 1024 × 1024 rounded-square application icon. |
| `app-icon-small.svg` | Simplified icon for 16–32 px (favicon, tray, taskbar). |
| `wordmark.svg` | Symbol + "CredOrbit", transparent. Follows `prefers-color-scheme`. |
| `wordmark-dark.svg` / `wordmark-light.svg` | Wordmark on a fixed dark or light panel. |
| `wordmark-tagline.svg` | Wordmark with the tagline (README header). |
| `social-card.svg` | 1280 × 640 GitHub/social preview. |
| `png/app-icon-512.png`, `png/logo-symbol-256.png` | Raster exports of the icon and symbol. |

## Construction

All symbol artwork sits on a 256 × 256 grid:

- Orbit ellipse: centre (128, 128), radii 104 × 64, rotated −24°. The opening
  spans ±40° around the ellipse's right vertex.
- Ring stroke: 14 units (5.5 % of the grid) with round caps.
- Motion arc: the same ellipse from −110° to −40°, cyan, fading in toward the
  leading node.
- Trail: a concentric ellipse (120 × 78) from −100° to −55°, 4 units wide.
- Nodes: leading r 12 at −40°, far r 9 at 165°, tail r 8 at +40°. A knockout
  gap of 5–7 units is cut into the ring around each node.
- Core: r 20, with a halo ring at r 34 (3 units) and a soft glow out to r 52.
- The artwork is shifted by (+4, +4.5) so that its visual bounding box is centred.

**Small sizes (≤ 32 px)** use `app-icon-small.svg`: a thicker ring (10 % of the
grid), only the leading node, a solid core, and no halo, glow, trail or
knockouts.

## Palette

| Role | Name | Hex | Notes |
|------|------|-----|-------|
| Background | Deep Space | `#0B1020` | Primary dark background, ink on light |
| Surface | Surface | `#111C31` | Panels, cards, tiles |
| Structure | Orbit Blue | `#2563EB` | The ring, borders, primary actions |
| Highlight | Electric Cyan | `#22D3EE` | Core, leading node, focus, "Orbit" in the wordmark |
| Status | Success | `#34D399` | Valid credentials, completed sync |
| Status | Warning | `#FBBF24` | Expiring sessions, stale profiles |
| Status | Danger | `#F87171` | Revoked access, failures |
| Text | Text | `#F8FAFC` | Primary text on dark |
| Text | Muted | `#94A3B8` | Secondary text, tagline |

Light-background equivalents (also `brand.Light()` in Go): Orbit Blue `#1D4ED8`,
Cyan `#0891B2`, Success `#047857`, Warning `#B45309`, Danger `#B91C1C`,
Text `#0B1020`, Muted `#475569`, Surface `#F1F5F9`.

Supporting tints used only inside gradients: `#3B82F6` and `#1E40AF` (ring
shading), `#A5F3FC`, `#67E8F9`, `#0EA5C6` and `#0E7490` (core shading).

## Typography

- **Brand and marketing:** Space Grotesk (preferred) or Sora, bold (700) for the
  wordmark with letter-spacing of −2.5 %, and medium (500) for the tagline,
  set in caps with wide tracking (+23 %). The SVGs fall back through
  `Inter, system-ui, sans-serif`. Convert text to outlines before using it in
  print or anywhere fonts cannot be guaranteed.
- **Wordmark colours:** "Cred" in Text (`#F8FAFC`) on dark or ink (`#0B1020`) on
  light. "Orbit" uses a gradient from cyan to blue.
- **Terminal UI:** the TUI uses the terminal's own monospace font. The brand
  appears there as the Unicode mark (`brand.Mark(true)`), with an ASCII
  fallback, and the spaced-caps wordmark `C R E D O R B I T`.

## Clear space and minimum size

- **Clear space:** keep at least the diameter of the core halo (about 27 % of the
  symbol's height) clear on every side of the symbol or wordmark.
- **Minimum size:** 24 px (screen) or 8 mm (print) for the full symbol. Below
  24 px use `app-icon-small.svg`, which holds up down to 16 px. The wordmark
  needs at least 96 px of width.

## Do

- Use the provided files unmodified. Scale them proportionally.
- Use the full-colour mark on Deep Space, Surface or white. Use the mono mark on
  photographs or on brand colours you do not control.
- Use the light variants on white or near-white backgrounds.
- Keep cyan for the core and highlights only. The structure is blue.

## Don't

- Don't close the ring. The opening on the right is what forms the "C".
- Don't rotate, skew or flip the mark, or change the tilt of the orbit.
- Don't add padlocks, keys, shields or clouds, and don't put the mark inside
  another shape. The app icon tile is the only container.
- Don't recolour the nodes with status colours. Success, warning and danger are
  for UI state, not identity.
- Don't add drop shadows, outlines or bevels. Don't use the glow on its own.
- Don't set "CredOrbit" in a different typeface or split it into two words.
