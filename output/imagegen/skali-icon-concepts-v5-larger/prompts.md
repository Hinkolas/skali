# Skali purple — slightly larger stack

Mode: built-in imagegen editing the approved purple pair. Requested change: enlarge the foreground symbol by approximately 8%, preserving the selected shape, colors, placement and proportions.

- [Default](01-skali-purple-default.png)
- [Dark](02-skali-purple-dark.png)

The default edit used [the approved default icon](../skali-icon-concepts-v4-purple/01-skali-purple-default.png). The final dark edit attached [the approved dark icon](../skali-icon-concepts-v4-purple/02-skali-purple-dark.png) for its palette and the enlarged default output for matching shape size. An earlier oversized dark attempt was discarded.

Both final outputs are original generated rasters. These remain concept assets; existing app assets were not replaced. The 8% value is the edit target, not a claim of a mathematically exact transform.

## default

```text
Use case: precise-object-edit
Asset type: Skali purple app icon, DEFAULT appearance, slight symbol-size refinement.
Input image: EDIT TARGET; the approved purple icon.
Primary request: make ONLY the central three-piece stack symbol slightly bigger, increasing its width AND height by approximately 8 percent (scale factor 1.08) around its current center. Preserve the square canvas dimensions. Do not enlarge or zoom the whole image; enlarge the foreground glyph only against its existing background.
Precise target: the current glyph spans about 57% of canvas width. New glyph should span about 61.5% of canvas width, with equal left and right margins about 19.25% each. It should remain comfortably inset from every canvas edge. This is a modest but visible size increase, not an oversized or edge-to-edge icon.
Group transform: scale the rounded top diamond and both rounded chevrons together as ONE uniformly scaled group. Their mutual proportions, relative positions, gap ratios, diagonal angles, corner radii and chevron thickness ratios must remain identical to the original. Keep the group's optical center fixed.
Color invariants: preserve the approved Skali violet background and the existing white, pale lavender and soft lavender fills exactly as closely as possible. Do not recolor, change tonal treatment or introduce a new gradient.
Composition/style invariants: identical simple flat TWO-DIMENSIONAL graphic, same shapes, same stack identity, no additional elements. Only foreground group scale changes. No 3D, bevel, extrusion, shadows, glow, reflections, glass, texture, outlines, text, letters, labels, frames or rounded outside icon corners. One full-bleed opaque square icon only.
```

## dark

```text
Use case: precise-object-edit
Asset type: Skali dark app icon, exact size matching.
Input images:
Image 1 is the approved DARK appearance: use its palette only.
Image 2 is the approved NEW slightly enlarged DEFAULT appearance: use its EXACT foreground geometry, scale and placement.
Primary request: produce the DARK-colored companion to Image 2. The output should be geometrically identical to Image 2, with only its colors changed to those of Image 1. COPY IMAGE 2'S FOREGROUND SHAPE BOUNDARIES AND POSITIONS EXACTLY. Do not rescale or enlarge Image 2 at all.
Shape specification from Image 2: the stack group is approximately 770 pixels wide within a 1254 square; horizontally from x=242 to x=1012. Vertically about y=230 to y=1024. Keep those same relative boundaries if output resolution differs. One top rounded diamond with two separated rounded downward chevrons. All curves, gaps, widths, heights and center positions must match Image 2 exactly.
Colors from Image 1: near-black purple-charcoal background, pale lavender top diamond, medium lavender middle chevron, vivid purple bottom chevron. No white symbol. Preserve Image 1's colors as faithfully as possible.
Only transfer the DARK palette of Image 1 onto the exact slightly larger glyph composition of Image 2. Do not change shape construction, spacing, margins, overall symbol scale or style.
Flat TWO-DIMENSIONAL graphic with clean solid fills. No 3D, depth, gloss, lighting, shadows, bevel, glow, reflections, glass, added gradients, texture, text, outline, frames, phone mockups or rounded outer corners. One opaque square icon only.
```
