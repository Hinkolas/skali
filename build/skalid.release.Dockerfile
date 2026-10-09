# Release image for skalid, used by goreleaser: the binary is prebuilt and
# copied into the build context, so this file only assembles the runtime
# layer. Alpine (not scratch) on purpose: the local bundle's bootstrap user
# Job pipes a generated password into `skalid user create` through a shell.
# Pinned by digest (the multi-arch index); bump the tag and digest together.
# Pulled through ECR Public's mirror of the official image (same digest):
# Docker Hub's anonymous pull limit on shared runners failed releases.
FROM public.ecr.aws/docker/library/alpine:3.24.2@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6
RUN apk add --no-cache ca-certificates
COPY skalid /usr/local/bin/skalid
EXPOSE 7070
ENTRYPOINT ["skalid"]
