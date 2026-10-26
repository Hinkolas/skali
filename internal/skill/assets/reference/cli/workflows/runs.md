# Logs and runs

Read with `skali skill read cli workflows runs`.
[CLI overview and command index](../../cli.md).

`skali logs [service]` streams live runtime output from an environment; without
a service it merges application logs. `skali run list` lists the environment's
journal. `skali run show <run-id>` prints an operation's steps;
`skali run logs <run-id> --step <step-key>` reads that step's recorded output
(for example `artifacts.web.build`), not the live application's log.
`skali run attach <run-id>` watches a run until it settles, and `skali run
cancel <run-id>` cancels after confirmation. Attaching supports `d` to leave
the run executing and two Ctrl-C presses to cancel. Run IDs and deployment
revision IDs are not interchangeable.
