# Skali for Apple platforms

The native Skali app: a SwiftUI client for a cluster's API, starting with
iPhone and iPad.

```
apps/apple/
├── Skali.xcodeproj    the app project (folders are synchronized: add files on disk, no project edits)
├── Skali/             the iOS/iPadOS app target: SwiftUI screens and app state
├── SkaliKit/          Swift package shared by every Apple target: API client, models,
│                      browser sign-in, Keychain storage, presentation rollups, fixtures
└── Config/            Info.plist additions and the base build configuration
```

There is one project for all Apple platforms. Everything below the UI lives
in `SkaliKit`, which builds for iOS, macOS and watchOS, so new targets (a Mac
app, a watch app, widgets) only add screens. The Mac should get its own
SwiftUI target with Mac idioms (sidebar, tables, menus, several windows)
rather than a stretched iPad app; it still shares `SkaliKit`.

## Requirements

- Xcode 27 or later
- iOS and iPadOS 26 or later (the Liquid Glass baseline)

## Build and run

Open `Skali.xcodeproj` and run the `Skali` scheme, or from the command line:

```sh
xcodebuild -project apps/apple/Skali.xcodeproj -scheme Skali \
  -destination 'generic/platform=iOS Simulator' build
```

Simulator builds need no signing. To run the app on a device, put your team
in `Config/Local.xcconfig`, which git ignores:

```
DEVELOPMENT_TEAM = ABCDE12345
```

If your signing setup needs a different bundle identifier, add it to the same
file:

```
PRODUCT_BUNDLE_IDENTIFIER = dev.example.skali
```

The shared default is `dev.hinke.skali`. Do not change the team or bundle
identifier under Signing & Capabilities: Xcode writes those choices into
`project.pbxproj`, which can block later pulls. Use `Local.xcconfig` instead.

Launch arguments (Edit Scheme > Run > Arguments):

- `-SkaliDemo` runs against built-in fixtures instead of a cluster, with
  nothing persisted. Use it for UI work and screenshots.
- `-SkaliDemoMember` does the same, signed in as a member instead of an
  instance admin.
- `-SkaliDemoUpdate available` sets where the demo cluster's software update
  stands: `current` (the default), `available`, `updating` or `failed`. An
  update the demo runs advances with the clock, about a minute and a half
  from the first node to verified.
- `-SkaliDemoBackups none` starts the demo cluster without a backup target.
- `-SkaliTab settings` (Debug builds only) opens on the given tab: `projects`,
  `settings` or `search`.
- `-SkaliProject birefnet-proxy` (Debug builds only) opens the project with
  that name once the list has loaded; `-SkaliProjectRoute deployments` (or
  `backups`, or a service as `application:<key>`, `database:<key>` or
  `bucket:<key>`) goes on to that page. `-SkaliEnvironment staging` shows
  that environment, and `-SkaliProjectSheet promote` (or `redeploy`,
  `delete`, or `accept` where a failed restore holds the environment, as in
  the demo's `nordlicht-api` staging) opens that sheet on the overview.
- `-SkaliRoute softwareUpdate` (Debug builds only) pushes a Settings screen:
  `account`, `users`, `nodes`, `databases`, `backups`, `softwareUpdate`,
  `node:<name>` for one node, `pool:<name>` for one database pool, or
  `user:<id>` for one user.

## Admins and members

The second tab follows the instance role. Instance admins get **Settings**,
which has the cluster's administration (nodes, databases, backups, updates)
and links to their account. Members get **Account** as the tab itself,
because everything else under Settings is admin-only on the daemon.

## Tests

`SkaliKit` carries the unit tests and runs them on the Mac:

```sh
cd apps/apple/SkaliKit && swift test
```

`LiveClusterTests` decodes every read the app makes against a real daemon.
These tests only run when you point them at a cluster with an admin token:

```sh
SKALI_LIVE_URL=http://localhost:7070/api SKALI_LIVE_TOKEN=$(skali remote token) swift test
```

## Sign-in

The app signs in the same way `skali remote add` does, with the device
authorization flow (`/v1/auth/device/*`):

1. The app opens a device request.
2. It shows the Studio's `/auth/device?code=…` page in an
   `ASWebAuthenticationSession` sheet.
3. It polls until someone approves the request.

The sheet shares Safari's cookies. Someone already signed in to the Studio
only has to press Approve. Someone who is not signed in gets Safari's password
autofill for the cluster's own domain. The app could never offer that autofill
itself, because every cluster lives on a different host.

The browser never hands a credential to the app. The session only arrives in
the poll response, and it is stored in the Keychain (this device only, and
only readable while the device is unlocked).

Sudo-gated changes, such as the update settings, use the same sheet. On
`403 reauth_required` the app opens a reauth request
(`/v1/auth/device/requests/reauth`), waits for the approval, and retries.

Today the approval page does not redirect anywhere, so the sheet closes when
the next poll sees the approval, within the poll interval. Making the Studio
send the browser to `skali://…` after Approve would close it at once.

### Against a local daemon

App Transport Security allows local networking, so a development daemon
works. The sign-in page comes from the Studio, so point the app at the Vite
dev server, which proxies `/api` to the daemon:

```sh
task dev           # API on :7070
task dev:studio    # Studio on :5173
```

Then sign in to `http://localhost:5173/api`. An explicit URL is used as given.
A bare hostname becomes `https://<host>/api`, as in the CLI.
