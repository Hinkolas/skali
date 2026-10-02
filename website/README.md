# website

The public site at [skali.dev](https://skali.dev): marketing pages and, later,
the user documentation. Every page is prerendered by `@sveltejs/adapter-static`
and deployed to GitHub Pages. There is no server code; anything a page needs
must be known at build time.

```sh
npm ci
npm run dev      # or `task dev:website` from the repository root
npm run build    # writes build/
npm run check && npm run lint
```

## Files served verbatim

Besides the pages, the site serves files that live elsewhere in this
repository. `scripts/assets.mjs` copies them into `static/` before every
`dev` and `build` run; the copies are gitignored, so edit the originals.

| URL                       | Source                               |
| ------------------------- | ------------------------------------ |
| `/install.sh`             | `install.sh` in the same checkout    |
| `/schemas/v1/<name>.json` | `schemas/` at the newest release tag |

- **install.sh** follows `main`: the script resolves the release to install
  itself (`SKALI_CHANNEL`, `SKALI_VERSION`), so the newest copy is always the
  right one.
- **Schemas** follow the newest release, not `main`, because `main` may
  already describe fields the released binaries reject. The newest stable tag
  wins; while there is none, the newest prerelease is used. The build fails
  if a schema's `$id` is not the URL it is served at, so the `$id` is always
  a working link. Set `SCHEMA_REF` to any git ref to override the tag, e.g.
  `SCHEMA_REF=HEAD npm run dev` to preview unreleased schema changes.

The build needs the release tags. A shallow clone fails with a hint; fetch
the tags or set `SCHEMA_REF`.

The home page reads the tags too (`src/lib/server/release.ts`): the version
in the navigation is the newest release.

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
`npm run check`, `npm run lint`, and `npm run build` as the `website` job in
`ci.yml`.

The custom domain is configured in the repository's Pages settings, not with
a `CNAME` file, because the site is deployed from Actions.
