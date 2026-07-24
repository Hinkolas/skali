# R0 workflow transcripts

These transcripts are R0 design fixtures for REWORK_V2 sections 11 and 14.
They show complete terminal sessions for the workflows the v2 product must
implement, before any of the underlying commands exist.

What is binding:

- Workflow ownership: which tool performs which action, and what each tool
  refuses to do.
- Safety behavior: what requires confirmation, what non-interactive use must
  state explicitly, which failures leave which state untouched, and the exact
  scope of destructive actions.
- Information display: values and secrets are never printed; plans show
  destructive consequences; failures name the unchanged state.
- The run/step/attempt shapes, statuses, and guarantees from REWORK_V2
  section 9.

What may drift during implementation: exact wording, spacing, spinner and
duration cosmetics, and identifier formats. A behavioral deviation from these
transcripts is a design change and belongs in REWORK_V2 first.

Interactive examples use the settled prompt shape: `◆` introduces a question,
`└` keeps its answer in the transcript, and secrets settle as `entered`.
While a prompt is active, `│` connects its rows and a muted hint names the
keys: arrows navigate and edit, Space toggles a multi-select, and Enter
submits. Choice prompts keep their complete option list visible. Piped input
and `TERM=dumb` use deterministic line/number prompts without ANSI sequences.
`SKALI_ACCESSIBLE=1` forces that renderer on a TTY; `NO_COLOR` keeps keyboard
interaction but removes color.

Files:

- `cli-remote.md`: the `skali remote` group: adding a remote with initial
  login and TOTP, listing and switching, status, re-login, the registry
  token, removal, and the reserved `local` remote.
- `cli-deploy.md`: remote plan and deploy, interactive environment and
  env-file selection, local and cloud builds, stored-value default, env-file
  upload, failure atomicity, detach/reattach, and destructive confirmation.
- `cli-dev.md`: the CLI-owned local installation: first run, repeat run,
  status, logs, stop, and reset.
- `cli-cluster.md`: the privileged `skali cluster` group: fresh single node,
  non-interactive configuration, multi-node join, cluster initialization,
  registry token authentication and the public registry domain, tier
  upgrade, version upgrade (k3s and bundle), repeat execution, degraded
  diagnosis, existing Kubernetes, scoped uninstall, and macOS host
  management (Lima VM with self-provisioned dependencies).

The example project throughout is `examples/file-sharing` (application `web`,
database `data`, bucket `files`, values `APP_DOMAIN` and secret
`SESSION_SECRET`).
