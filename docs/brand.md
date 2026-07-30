# The mark

![Feint](assets/brand/feint-lockup-light.svg)

The logo is two strokes and nothing else. A solid path climbs and then flattens:
that is the surface the emulator actually serves. A dashed path leaves the same
corner at the same angle and keeps climbing: that is what the provider announced
and nobody has driven yet. One gesture, interrupted.

It is the project's argument drawn rather than written — the surface is measured,
not followed, and the gap between the two lines is the thing this repository
exists to keep visible.

## Files

Everything is in [`assets/brand/`](assets/brand/), SVG only.

| File | Use |
|---|---|
| `feint-lockup-light.svg` | the default, on light backgrounds |
| `feint-lockup-dark.svg` | on dark backgrounds — the green lifts, it is not the same green |
| `feint-lockup-mono.svg` | single ink; inherits `currentColor` |
| `feint-icon.svg` / `feint-icon-dark.svg` | the mark alone, from 24px up |
| `feint-icon-mono.svg` | the mark alone, single ink, for favicons and terminals |

## How it is embedded, and why that way

GitHub switches on the reader's theme through `<picture>`, which is the supported
mechanism and the one worth copying:

```html
<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/assets/brand/feint-lockup-dark.svg">
  <img src="docs/assets/brand/feint-lockup-light.svg" alt="feint" width="230">
</picture>
```

**The wordmark is outlines, not text**, and that is what makes this safe. An SVG
carrying `<text font-family="Poppins">` renders in whatever the reader's browser
has — Helvetica, Arial, DejaVu — because GitHub has no webfont to give it, and a
lockup set in the wrong grotesque reads as a mistake. The five glyphs are Poppins
SemiBold converted to curves, so the file is self-contained: same rendering on
GitHub, in a slide, in a PDF, on a machine that has never heard of the typeface.

Poppins is under the SIL Open Font License. Only these five glyph shapes travel
in this repository, not the font.

## Two things this repository cannot version

- **The social preview**, the card shown when the repository link is shared. It
  is a GitHub setting rather than a file: Settings → Social preview, a 1280×640
  PNG. Rasterise `feint-lockup-light.svg` on a background at 2x for it.
- **The account avatar**, which GitHub takes from the owner rather than from the
  repository.

**Editing the wordmark means regenerating it.** The curves come from Poppins
SemiBold through fontTools; they are not editable as text and must not be
re-typed by hand. The icon files carry no text at all and can be edited directly.

## Using it

- **Clear space**: the height of the `f` on every side. Nothing enters it.
- **Smallest sizes**: 24px for the lockup, 16px for the icon. Below 16px the
  three dashes stop reading as three and the mark loses its whole point.
- **The dashes are exactly three.** Not two, not five. The dash array is tuned to
  the stroke length so that scaling the SVG keeps them at three; redrawing the
  path by hand does not.
- **On dark backgrounds, use the dark file.** The light green is legible on white
  and muddy on navy, which is why there are two.

Do not recolour the mark to a single colour except through
`feint-*-mono.svg`, do not separate the two strokes, do not set the wordmark in
another typeface, and do not add effects — a shadow on a two-stroke mark reads as
a rendering bug.

## Licence

**The name *Feint* and the logo are not covered by the Apache 2.0 licence** that
covers the code. They may be used to refer to this project — in an article, a
talk, a comparison, a list of tools — without asking. They may not be used as the
mark of a fork, a product or a service, or in a way that suggests the project
endorses something it does not.

This is the ordinary split for an open-source project, and it is stated here
because a reader who wants to do the right thing should not have to guess.
