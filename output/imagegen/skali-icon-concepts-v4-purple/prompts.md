# Skali purple — default and dark stack icons

Mode: built-in imagegen, with the selected indigo pair attached as edit targets. This round changes the palette to existing Skali purples while preserving the selected flat stack concept. Current application assets have not been replaced.

1. [Default](01-skali-purple-default.png)
2. [Dark](02-skali-purple-dark.png)

Color sources:

- Native app: `apps/apple/Skali/Assets.xcassets/AccentColor.colorset/Contents.json` — #6D4EF6 default and #A58FFF dark.
- Website/Studio: #7C5CFF accent, #9076FF gradient start, #5B33FF gradient end.

Prompt targets, ordered background / top diamond / middle chevron / bottom chevron:

- Default: #6D4EF6 / #FFFFFF / #E4DCFF / #C7B7FF.
- Dark: #18151F / #CDBFFF / #A58FFF / #9076FF.

Original generated 1254 × 1254 rasters are preserved. Hex values are prompt targets, not guaranteed exact generated pixels. These remain concept studies; a production vector master would ensure precise colors and shared geometry.

## default

```text
Use case: precise-object-edit
Asset type: Skali iOS app icon, DEFAULT appearance purple refinement. Target 1024 x 1024 square.
Input image: EDIT TARGET: selected Ink Indigo / default icon from the previous exploration.
Primary request: RECOLOR ONLY the exact provided icon to Skali's existing native iOS brand purple. This is a strictly controlled palette change, not a redesign. Preserve every single shape and every edge position from the input: the large rounded diamond, two rounded downward chevrons, matching diagonal angles, generous negative-space gaps, exact glyph scale, optical center and all margins.
Actual project color grounding: the native iOS AccentColor default is #6D4EF6; dark-mode accent is #A58FFF. Website/Studio also use #9076FF as a light gradient stop. Use the native default accent #6D4EF6 for the entire uniform background.
Colors: background solid flat vivid Skali violet #6D4EF6, visibly purple rather than blue-indigo. Top rounded diamond solid white #FFFFFF. Middle chevron solid very pale lavender #E4DCFF. Bottom chevron solid soft lavender #C7B7FF. Maintain the gentle three-step tonal progression from white to lavender.
Style: immaculately clean flat TWO-DIMENSIONAL app icon graphic. Minimal and polished, with smooth geometry and crisp edges. Preserve the exact selected graphic.
Constraints: change COLORS ONLY. Do not change symbol geometry, gap widths, shape positions, corners, sizes or composition. No 3D, extrusions, shadows, gradients, highlights, glow, lighting, bevels, glass, texture, glossy effects, outlines, text, labels, letters, frame, badge, phone mockup or presentation board. No rounded OUTER square corners baked in. One opaque full-bleed square icon only.
```

## dark

```text
Use case: precise-object-edit
Asset type: Skali iOS app icon, DARK appearance purple refinement. Target 1024 x 1024 square.
Input image: EDIT TARGET: selected Ink Indigo / dark icon from the previous exploration.
Primary request: RECOLOR ONLY the supplied exact flat icon into Skali's existing purple brand palette, as a dark companion to a native-purple #6D4EF6 default icon. Keep the background dark and put visibly lavender-to-violet COLOR onto the glyph. The icon itself is colored, not white.
Actual project grounding: Skali native iOS uses #6D4EF6 default accent and #A58FFF dark accent. Website and Studio also use #9076FF as a lighter brand purple. Use these existing purple hues as the visual anchors.
Colors: solid dark purple-charcoal background #18151F. Top rounded diamond solid pale violet-lavender #CDBFFF. Middle chevron solid Skali dark accent lavender #A58FFF. Bottom chevron solid vivid Skali purple #9076FF. All three shapes remain clearly visible and colored, with a controlled light-to-deep purple progression.
Invariants: STRICTLY preserve the exact existing geometry and every contour: one large rounded diamond and two separated rounded downward chevrons. Preserve sizes, same angles, smooth curves, gap widths, optical center, glyph scale and all margins. Change colors only, not the shape design.
Style: immaculately polished flat TWO-DIMENSIONAL vector-style graphic, simple clean uniform color regions.
Constraints: no white symbol, no material rendering, no 3D, extrusion, bevel, shadow, lighting, reflection, glass, gradients, texture or glow. No new shapes, outline, text, letters, labels, border, badge, frame, phone mockup or presentation board. No rounded outer square corners baked into image. One opaque full-bleed square icon only.
```
