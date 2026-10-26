# website

The public site at [skali.dev](https://skali.dev): marketing pages, the manifest
reference, and editor schemas. Every page is prerendered by `@sveltejs/adapter-static`
and deployed to GitHub Pages. There is no server code; anything a page needs
must be known at build time.

```sh
npm ci
npm run dev      # or `task dev:website` from the repository root
npm run build    # writes build/
npm test        # release selection and versioned schema publication
npm run check && npm run lint
```

## Files served verbatim

Besides the pages, the site serves files that live elsewhere in this
repository. `scripts/assets.mjs` copies them into `static/` before every
`dev` and `build` run; the copies are gitignored, so edit the originals.

| URL                              | Source                                               |
| -------------------------------- | ---------------------------------------------------- |
| `/install.sh`                    | `install.sh` in the same checkout                    |
| `/schemas/v1/<name>.json`        | `schemas/` at the newest release tag                 |
| `/schemas/<release>/<name>.json` | `schemas/` at that exact release tag, including RCs  |
| `/schemas/releases.json`         | Floating reference and available schema release tags |

- **install.sh** follows `main`: the script resolves the release to install
  itself (`SKALI_CHANNEL`, `SKALI_VERSION`), so the newest copy is always the
  right one.
- **Schemas** follow the newest release, not `main`, because `main` may
  already describe fields the released binaries reject. The newest stable tag
  wins; while there is none, the newest prerelease is used. The build fails
  if a schema's `$id` is not the URL it is served at, so the `$id` is always
  a working link. Set `RELEASE_REF` to any git ref to override the tag, e.g.
  `RELEASE_REF=HEAD npm run dev` to preview unreleased schema changes.
- **Pinned schemas** include the full release tag, for example
  `/schemas/v0.1.3-rc.10/skali.schema.json`. Each preserves the tag's validation
  rules, with its `$id` rewritten to the served URL and a provenance comment.
  Older tags with no schemas are omitted. This lets editors use an RC's
  vocabulary while the floating URL stays on stable. `RELEASE_REF` changes
  only the floating preview, never the pinned release content.

The build needs the release tags. A shallow clone fails with a hint; fetch
the tags or set `RELEASE_REF`.

## Manifest reference

`/docs/manifest` renders `internal/skill/assets/reference/manifest.md`, the
reference `skali skill read manifest` prints, from the same release as the
schemas (`src/lib/server/reference.ts`; `RELEASE_REF` applies too). There is
no copy to keep in sync: edit the reference in place, and the page picks it up
with the next release. Its complete manifests (fenced as `yaml manifest`) are
compiled and schema-checked by `internal/skill/content_test.go`, which also
requires every manifest field to appear in an example or have an explicit
non-deployable exception, and independently requires a prose row explaining
every key. The generated change-history section mirrors the embedded ledger.
The page's stable/prerelease guidance belongs to the website shell so it can
also clarify older reference text that predates pinned schemas.

The home page reads the tags too (`src/lib/server/release.ts`): the version
in the navigation is the newest release.
Here "newest" means the stable-first selection above; it does not mean the
highest RC tag while a stable release exists.

## Layout

- `src/lib/sections/`: the home page's sections, in page order in
  `src/routes/+page.svelte`.
- `src/lib/mock/`: static renderings of Skali Studio screens used as
  product pictures. They copy the Studio's sizes and colors by hand; the
  theme tokens in `src/routes/layout.css` mirror `studio/src/routes/layout.css`.

## Deployment

`.github/workflows/pages.yml` builds this directory and deploys `build/` to
the `github-pages` environment. It runs:

- on pushes to `main` that touch `website/`, `install.sh`, or the workflow;
- after every published release: the last job of `release.yml` dispatches it
  on `main`, so the site picks up the new release's schemas;
- by hand (`gh workflow run pages.yml`).

Deployments always run from `main`, even after a release, and the
`github-pages` environment accepts only `main`. Pull requests run
`npm test`, `npm run check`, `npm run lint`, and `npm run build` as the `website` job in
`ci.yml`.

The custom domain is configured in the repository's Pages settings, not with
a `CNAME` file, because the site is deployed from Actions.
