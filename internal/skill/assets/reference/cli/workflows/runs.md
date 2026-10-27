# Logs and runs

Read with `skali skill read cli workflows runs`.
[CLI overview and command index](../../cli.md).

`skali logs [service]` streams live runtime output from an environment; without
a service it merges application logs. `skali run list` lists the environment's
journal. `skali run show <run-id>` prints an operation's steps;
`skali run logs <run-id> --step <step-key>` reads that step's recorded output
(for example `release:web`, the release command), not the live application's
log. A local build's output is not journaled line by line: the CLI uploads one
insights report per build once it ended. `skali run insights <run-id>` prints
each build's outcome, duration, cached steps, and slowest steps, and `--log`
adds its full output; a build without a report says so.
`skali run attach <run-id>` watches a run until it settles, and `skali run
cancel <run-id>` cancels after confirmation. Attaching supports `d` to leave
the run executing and two Ctrl-C presses to cancel. Run IDs and deployment
revision IDs are not interchangeable.
