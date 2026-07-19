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

Files:

- `cli-deploy.md`: remote plan and deploy, local and cloud builds, env-file
  upload, remote-value reuse, failure atomicity, detach/reattach, and
  destructive confirmation.
- `cli-dev.md`: the CLI-owned local installation: first run, repeat run,
  status, logs, stop, and reset.
- `installer.md`: privileged installation: fresh single node, non-interactive
  configuration, multi-node join, cluster initialization, tier upgrade,
  repeat execution, degraded diagnosis, existing Kubernetes, and scoped
  uninstall.

The example project throughout is `examples/file-sharing` (application `web`,
database `data`, bucket `files`, values `APP_DOMAIN` and secret
`SESSION_SECRET`).
