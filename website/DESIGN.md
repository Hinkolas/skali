---
name: skali.dev
description: The public site for skali, a self-hosted application platform for your own servers.
colors:
  surface-base: '#0b0b0f'
  surface-input: '#0d0d11'
  surface-panel: '#0e0e12'
  surface-raised: '#121216'
  surface-card: '#17171c'
  surface-violet: '#0f0d1a'
  border-subtle: 'rgb(255 255 255 / 0.06)'
  border-default: 'rgb(255 255 255 / 0.07)'
  border-section: 'rgb(255 255 255 / 0.08)'
  border-raised: 'rgb(255 255 255 / 0.09)'
  border-strong: 'rgb(255 255 255 / 0.12)'
  text-primary: '#ececf1'
  text-secondary: '#c9cad4'
  text-tertiary: '#a9aab6'
  text-muted: '#9899a6'
  text-faint: '#84858f'
  text-ghost: '#64656f'
  accent: '#7c5cff'
  accent-light: '#a58fff'
  accent-nav: '#d7cdfe'
  accent-from: '#9076ff'
  accent-to: '#5b33ff'
  status-success: '#4ec98c'
  status-warning: '#d9a54f'
  status-danger: '#e0596e'
  service-app: '#a99bf8'
  service-db: '#68c4d8'
  service-storage: '#d9a54f'
  chart-2: '#b8802c'
typography:
  display:
    fontFamily: "'Geist Variable', ui-sans-serif, system-ui, sans-serif"
    fontSize: 'clamp(44px, 8vw, 96px)'
    fontWeight: 500
    lineHeight: 1
    letterSpacing: '-0.055em'
  headline:
    fontFamily: "'Geist Variable', ui-sans-serif, system-ui, sans-serif"
    fontSize: 'clamp(32px, 4.6vw, 52px)'
    fontWeight: 500
    lineHeight: 1.05
    letterSpacing: '-0.04em'
  title:
    fontFamily: "'Geist Variable', ui-sans-serif, system-ui, sans-serif"
    fontSize: 'clamp(28px, 3.2vw, 40px)'
    fontWeight: 500
    lineHeight: 1.1
    letterSpacing: '-0.03em'
  statement:
    fontFamily: "'Geist Variable', ui-sans-serif, system-ui, sans-serif"
    fontSize: 'clamp(26px, 3.4vw, 40px)'
    fontWeight: 400
    lineHeight: 1.3
    letterSpacing: '-0.03em'
  lead:
    fontFamily: "'Geist Variable', ui-sans-serif, system-ui, sans-serif"
    fontSize: 'clamp(17px, 1.8vw, 20px)'
    fontWeight: 400
    lineHeight: 1.6
  body:
    fontFamily: "'Geist Variable', ui-sans-serif, system-ui, sans-serif"
    fontSize: '17px'
    fontWeight: 400
    lineHeight: 1.6
  body-sm:
    fontFamily: "'Geist Variable', ui-sans-serif, system-ui, sans-serif"
    fontSize: '15px'
    fontWeight: 400
    lineHeight: 1.6
  code:
    fontFamily: "'Geist Mono Variable', ui-monospace, 'SF Mono', monospace"
    fontSize: '17px'
    fontWeight: 400
  label:
    fontFamily: "'Geist Mono Variable', ui-monospace, 'SF Mono', monospace"
    fontSize: '12px'
    fontWeight: 400
  label-caps:
    fontFamily: "'Geist Mono Variable', ui-monospace, 'SF Mono', monospace"
    fontSize: '11px'
    fontWeight: 400
    letterSpacing: '0.08em'
rounded:
  control: '10px'
  bar: '16px'
  panel: '22px'
  feature: '28px'
  pill: '9999px'
spacing:
  gutter: '24px'
  header-column: '220px'
  header-gap: '48px'
  row-gap: '120px'
  section-y: '140px'
components:
  button-primary:
    backgroundColor: '{colors.text-primary}'
    textColor: '{colors.surface-base}'
    rounded: '{rounded.control}'
    height: '48px'
    padding: '0 22px'
  button-primary-hover:
    backgroundColor: '#ffffff'
  button-ghost:
    backgroundColor: 'transparent'
    textColor: '{colors.text-secondary}'
    rounded: '{rounded.control}'
    height: '48px'
    padding: '0 22px'
  command-bar:
    backgroundColor: '{colors.surface-base}'
    textColor: '{colors.text-primary}'
    typography: '{typography.code}'
    rounded: '{rounded.bar}'
    padding: '10px 10px 10px 24px'
  feature-panel:
    backgroundColor: '{colors.surface-panel}'
    rounded: '{rounded.panel}'
    padding: 'clamp(20px, 3vw, 40px)'
  install-card:
    backgroundColor: '{colors.surface-violet}'
    rounded: '{rounded.feature}'
    padding: 'clamp(28px, 6vw, 80px)'
  section-label:
    textColor: '{colors.text-faint}'
    typography: '{typography.label}'
  nav-link:
    textColor: '{colors.text-muted}'
  nav-link-hover:
    textColor: '{colors.text-primary}'
---

# Design System: skali.dev

## Overview

**Creative North Star: "The Honest Console"**

The site is a quiet frame for the real product. Real Studio screens, real commands, real manifest behavior: everything that carries weight on the page is something skali actually does, rendered faithfully. The chrome around it stays low-lit and restrained so the product pictures and the commands read as the evidence they are. This is the visual form of the product's honest-alpha stance: credibility comes from showing, not from adjectives, logos or borrowed proof.

The mood is precise, calm and candid. A single dark theme of near-black surfaces separated by hairline white-alpha borders, a long graded text ramp that does most of the hierarchy work, and one violet accent (Studio Violet) that appears sparingly enough to act as a signal. Type is Geist throughout: large, tightly tracked medium-weight headlines against muted, comfortably leaded body copy, with Geist Mono carrying labels, commands and versions. Density is generous at the section level (140px of vertical air) and tight inside the product pictures, which reproduce Studio at its own scale.

The theme mirrors `studio/src/routes/layout.css` on purpose (it also defines `surface-node` and `service-temp`, unused on the site so far), so a visitor who installs skali recognizes the Studio they then open.

**Key Characteristics:**

- One dark theme only (`color-scheme: dark`); no light mode.
- Depth from tonal surface steps and hairline borders, not shadows.
- Studio Violet is rare: section indices, the hero halo, the install card, focus rings, the logo tile.
- Product pictures are hand-built Studio mocks, labelled as images, faded out at their lower edge.
- Mono type marks everything machine-real: commands, versions, labels, environment names.

## Colors

A near-black neutral world with a long text ramp, one violet accent, and a small set of category hues borrowed from Studio.

### Primary

- **Studio Violet** (`accent`): the brand accent. Used for the hero halo and frame glow, the install card's wash and border (at 26% alpha), and selection tint. Never a large solid fill on site chrome.
- **Studio Violet Light** (`accent-light`): the accent at text and line weight. Section indices (`01`, `02`), the URL inside the install command, workflow timeline dots, and the focus ring.
- **Lavender Mist** (`accent-nav`): the palest accent tint, used for the active sidebar item and avatar initials inside the Studio overview mock.
- **Logo Gradient** (`accent-from` to `accent-to`, 135°): reserved for the logo tile and the favicon.

### Tertiary

Category hues come from Studio and keep Studio's meaning. Use them only to label the thing they stand for.

- **Application Lilac** (`service-app`): applications; also the eyebrow on the Deployments row.
- **Database Teal** (`service-db`): Postgres; the eyebrow on the Data row and its panel wash.
- **Storage Amber** (`service-storage`): buckets and storage; the eyebrow on the Operations row.
- **Healthy Green / Warning Amber / Danger Rose** (`status-success`, `status-warning`, `status-danger`): status dots and badges in the mocks (`protected` uses warning).
- **Chart Bronze** (`chart-2`): the outbound series beside Studio Violet in the Studio overview chart.

### Neutral

- **Night Base** (`surface-base`): the page background and `theme-color`; also the text color of inverted buttons.
- **Input Black / Panel Black** (`surface-input`, `surface-panel`): command bars, code wells and the feature panels in Platform.
- **Raised / Card Graphite** (`surface-raised`, `surface-card`): Studio's own surface steps, used inside the mocks.
- **Violet Night** (`surface-violet`): the install card's ground, a violet-shifted black.
- **Hairline ramp** (`border-subtle` 6% to `border-strong` 12% white): every divider and container edge. Subtle for footers and mock internals, section for fact lists and dividers, default for feature panels, strong for floating elements and the command bar.
- **Text ramp** (`text-primary` to `text-ghost`): primary for headlines and emphasis, secondary for ghost-button labels, tertiary for supporting copy on cards, muted for body and leads, faint for labels and meta, ghost for de-emphasized words and shell prompts.

### Named Rules

**The Signal Rule.** Studio Violet is an indicator, not a paint. If a screen's violet would read as a fill rather than a light, it is too much.

**The Borrowed Meaning Rule.** Category hues mean what they mean in Studio (lilac is apps, teal is databases, amber is storage). Never use them decoratively or swap their meaning.

**The Ramp Does the Work Rule.** Hierarchy comes from stepping along the text ramp before it comes from size or weight. Emphasis inside a muted paragraph is a step up to `text-primary`, not bold.

## Typography

**Display Font:** Geist Variable (with ui-sans-serif, system-ui)
**Body Font:** Geist Variable
**Label/Mono Font:** Geist Mono Variable (with ui-monospace, SF Mono)

**Character:** One family in two voices. Geist sans carries the argument in large, tightly tracked medium weights; Geist Mono marks what is literal and machine-real.

### Hierarchy

- **Display** (500, clamp 44–96px, line-height 1, tracking −0.055em): the hero headline only, with a deliberate line break.
- **Headline** (500, clamp 32–52px, 1.05, −0.04em): section headings beside the numbered label. The install heading runs slightly larger and tighter (clamp 34–56px, 1.02, −0.05em) with balanced wrapping.
- **Title** (500, clamp 28–40px, 1.1, −0.03em): the headings of the Platform rows.
- **Statement** (400, clamp 26–40px, 1.3, −0.03em): the Under the hood paragraph, set in `text-ghost` with the named components lifted to `text-primary`. As it scrolls up the screen it is read out word by word from a dimmer gray, ending exactly in that state.
- **Lead** (400, clamp 17–20px, 1.6): the hero paragraph, max ~520px, in `text-muted`.
- **Body** (400, 17–18px, 1.6–1.65): section leads and row copy, max ~500–560px.
- **Body small** (400, 15px, 1.6): workflow step text and fact lists.
- **Code** (Mono 400, 17px; 15px in the install bar, 13px on phones): commands, shown verbatim.
- **Label** (Mono 400, 12–13px): section labels, eyebrows, platform meta. Footer column titles use 11px uppercase with 0.08em tracking.

### Named Rules

**The Literal Is Mono Rule.** Anything a user could type or read back from the product (commands, versions, environment names, `skali.yaml`) is set in Geist Mono. Nothing else is.

**The Medium Ceiling Rule.** Headlines top out at weight 500 and get their presence from size and negative tracking. Semibold is reserved for the wordmark.

All headings and paragraphs use `text-wrap: pretty`.

## Layout

A single centered column per section with a 24px side gutter, stacked with 140px of vertical padding. Containers vary by section on purpose: 1200px for the hero copy, Under the hood and the footer; 1320px for Workflow, Platform and the install card; 1152px for section headers inside the wider sections, so their labels align with the rows below; 1344px for the hero's product frame.

Section headers are a two-part row: a 220px mono label column (index in violet, label in faint) and the heading column beside it, with a 48px gap. On narrow screens the label wraps above the heading.

Platform alternates copy and picture in flex rows (80px column gap, 120px between rows) that stack once copy (400px basis) and picture (480px basis) no longer fit side by side. From 1280px up, each picture stops 64px short of the outer edge on its side so it never lines up with the text column. Workflow is an ordered timeline: one column on phones, two from 768px, four from 1280px, each step drawing its own stretch of the rule.

Sizing uses `clamp()` for type and panel padding rather than breakpoint jumps. Breakpoints in use are Tailwind's `sm` (640px), `md` (768px) and `xl` (1280px). Phones show the hero frame's main Studio surface rather than its sidebar.

## Elevation & Depth

Tonal first, lift on float. Surfaces sit flat and are separated by stepping through the near-black ramp and by hairline white-alpha borders. Soft violet light (the hero halo, radial washes in panel corners, the faint blueprint grid) adds atmosphere but never stands in for structure. Real shadows appear only under elements that genuinely float over others.

### Shadow Vocabulary

- **Floating dark** (`box-shadow: 0 30px 80px rgb(0 0 0 / 0.45)`): the install command bar over the install card.
- **Dialog dark** (`box-shadow: 0 24px 60px rgb(0 0 0 / 0.45)`): the promote dialog over the backups table.
- **Frame glow** (`box-shadow: 0 -20px 80px rgb(124 92 255 / 0.10)`): the hero's Studio frame, lit from behind.

### Named Rules

**The Flat Until It Floats Rule.** A surface earns a shadow only when it overlaps another surface. Panels, cards and rows at rest have borders, not shadows.

**The Light Is Atmosphere Rule.** Violet washes and halos sit behind content at low alpha (10–24%) and fade out by mask. They never outline, fill or replace a border.

## Shapes

Generous, consistent rounding on hairline-bordered containers; nothing sharp, nothing pill-shaped except dots and badges. Radius grows with the container's size: controls 10px, the command bar and dialogs 16px, feature panels and the hero frame 22px (the frame rounds only its top corners and runs off the bottom of the section), the install card 28px. Status dots, timeline dots and badges are fully round. Mocks keep Studio's own radii (15px cards, 11px list rows, 6px keycaps) rather than the site scale.

The faint grid recurs as a motif: 72px behind the hero, 56px in the install card, both masked to fade from one corner or edge.

## Motion

The page moves the way the product does, and only where motion says something.

- **Opening:** the grid and halo come up, the headline arrives word by word out of a blur, the lead and actions follow, then the Studio frame rises into the light and, once it has settled, its charts draw. Plain CSS on load, about 4s end to end, ease-out (`cubic-bezier(0.16, 1, 0.3, 1)`).
- **Live mocks:** the overview's charts stream samples and the deployment run plays through to `succeeded` and loops, both using Studio's own motion (spinner, progress sheen, step clocks).
- **Scroll-linked:** the workflow trace and the Under the hood read-through follow scroll position, so they run as fast as the visitor reads.
- **Reveals:** section headers, Platform copy and panels, workflow steps and the install card rise 28px into place once, via `$lib/motion`'s `reveal`. Only what is below the fold at hydration is held back. The promote dialog opens (scale from 0.96, slight blur) rather than rises.
- **Small feedback:** arrows on the hero and install links nudge 2px on hover; the install command's block caret blinks six times, then rests.

### Named Rules

**The Already Visible Rule.** The default render is the finished state. Scripts only ever hold back what is offscreen, so a page whose scripts fail is whole.

**The Reduced Motion Rule.** With reduced motion the page is the prerendered still: no opening, no streaming, no reveals, no traces; the dots, statement and mocks show their designed state.

**The Offscreen Rule.** Loops pause when offscreen or in a hidden tab.

## Components

### Buttons

Quiet and exact. One loud control, one quiet one.

- **Shape:** gently rounded (10px), 48px tall, 22px horizontal padding, 15px medium label.
- **Primary:** inverted, `text-primary` fill with `surface-base` text; hovers to pure white. Optional trailing arrow icon at 16px.
- **Ghost:** transparent with a 14% white border and `text-secondary` label; hover raises the border to 28% and the label to white.
- **Copy button:** the primary treatment at 46px inside the command bar, swapping its icon and label to a check and "Copied" for 1.6s, with an `aria-live` announcement.
- **Focus:** the global 2px `accent-light` outline at 3px offset.

### Command bar

The signature control: the install command as a real, copyable line.

- **Shape:** 16px radius, `border-strong` hairline, `surface-base` at 88% over the install card, floating dark shadow.
- **Content:** a ghost `$` prompt that is not selectable, the command in Mono with the URL in `accent-light`, and the copy button flush right.
- **Responsive:** one line from 768px; on phones the command wraps at its spaces instead of scrolling.
- **Fallback:** without clipboard access the command is selected for copying by hand.

### Cards / Containers

- **Feature panel:** 22px radius, `border-default`, `surface-panel`, padding clamp 20–40px, one radial wash in a corner tinted by the row's category hue (violet, teal or amber at 10–14%). Holds Studio mocks and is labelled `role="img"` with a descriptive `aria-label`.
- **Install card:** 28px radius, Studio Violet border at 26%, `surface-violet` ground, a violet wash and a masked 56px grid from the top-left corner. Padding clamp 28–80px.
- **Fact list:** borderless rows in `body-small` and `text-tertiary`, separated by `border-section` hairlines top and bottom.

### Navigation

- **Bar:** 72px tall, inside the hero, over the blueprint grid. Wordmark (logo tile and "skali" at 17px semibold, −0.02em) at left, text links at 14px in `text-muted` that brighten to `text-primary` on hover.
- **Right side:** the newest release tag in Mono 12px `text-faint`, and a 44px GitHub icon target.
- **Mobile:** the text links and version hide below 640px; the wordmark and GitHub icon remain.

### Section label

The numbered mono label (`01 Workflow`, `03 Under the hood`): index in `accent-light`, label in `text-faint`, 12px. It pins the section's place in the page's sequence and is the only recurring use of violet in body sections.

### Workflow timeline

An ordered list of steps on a shared 10% white rule, each with a 7px violet dot on the rule, a Mono label in faint, the command in Mono 17px primary, and a 15px muted explanation. A violet trace with a lit tip runs along the rule as the list scrolls up the screen, lighting each dot as it arrives (one stretch per step; on phones each step follows its own scroll). Without scripts or with reduced motion, the dots are simply lit.

### Studio mocks

Hand-built renderings of Studio screens (`src/lib/mock/`). They copy Studio's sizes, radii and colors exactly, including arbitrary pixel values, and are never simplified into generic illustration. Two of them move the way Studio does: the deployment run plays on from its prerendered frame once it is in view (Studio's spinner, progress sheen and step clocks), succeeds, and loops to a new commit; the overview's charts draw in when they come into view and then stream samples. Motion pauses offscreen and in hidden tabs, and with reduced motion the prerendered frame stays as it is. Each is wrapped by a container with `role="img"` and an `aria-label` describing what the screen shows, and is faded at its lower edge where it runs off the panel.

## Do's and Don'ts

### Do:

- **Do** show the product: real commands, real Studio screens, real manifest behavior, rendered at Studio's own fidelity.
- **Do** keep Studio Violet to indices, focus, the hero glow, the install card and the logo tile.
- **Do** separate surfaces with the hairline ramp (6–12% white) and tonal steps before reaching for a shadow.
- **Do** set commands, versions, environment names and labels in Geist Mono.
- **Do** keep headings at weight 500 with negative tracking (−0.03 to −0.055em) and body at 1.6 line-height in `text-muted`.
- **Do** mirror `studio/src/routes/layout.css` when a theme token changes, so the site and Studio stay one palette.

### Don't:

- **Don't** add a light theme, white sections or full-bleed violet fills.
- **Don't** use category hues (lilac, teal, amber) for anything but the service kind they stand for.
- **Don't** put shadows on resting panels, cards or rows.
- **Don't** replace Studio mocks with abstract illustration, stock imagery or invented dashboards showing features skali lacks.
- **Don't** use bold or semibold for emphasis in running text; step up the text ramp instead.
- **Don't** let violet light outline or fill a container; washes stay behind content at low alpha.
