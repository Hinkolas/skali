# Release image for skalid, used by goreleaser: the binary is prebuilt and
# copied into the build context, so this file only assembles the runtime
# layer. Alpine (not scratch) on purpose: the local bundle's bootstrap user
# Job pipes a generated password into `skalid user create` through a shell.
# The staged CLI builds (every platform, skali_<goos>_<goarch>) are what the
# daemon serves at /v1/system/cli so a member's skali can follow the
# cluster's version (docs/versioning.md, decision 2); SKALI_CLI_DIR names
# the directory. Dev and e2e builds keep using build/skalid.Dockerfile,
# which ships no CLI.
FROM alpine:3.21
RUN apk add --no-cache ca-certificates
COPY skalid /usr/local/bin/skalid
COPY .cache/skali-release/cli/ /usr/local/share/skali/cli/
EXPOSE 7070
ENTRYPOINT ["skalid"]
