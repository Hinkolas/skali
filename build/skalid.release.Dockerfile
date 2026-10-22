# Release image for skalid, used by goreleaser: the binary is prebuilt and
# copied into the build context, so this file only assembles the runtime
# layer. Alpine (not scratch) on purpose: the local bundle's bootstrap user
# Job pipes a generated password into `skalid user create` through a shell.
# Pinned by digest (the multi-arch index); bump the tag and digest together,
# in .github/mirrored-images.txt first. Pulled from our GHCR copy of the
# official image (same digest): anonymous pulls from public registries hit
# the shared runners' IP limits and failed releases.
FROM ghcr.io/hinkolas/mirror/alpine:3.24.3@sha256:b3413572228e34bd1c9245f5565e6cd127005655e8a7899aef33d8e83255c9e6
RUN apk add --no-cache ca-certificates
COPY skalid /usr/local/bin/skalid
EXPOSE 7070
ENTRYPOINT ["skalid"]
