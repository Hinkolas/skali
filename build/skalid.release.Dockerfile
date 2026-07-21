# Release image for skalid, used by goreleaser: the binary is prebuilt and
# copied into the build context, so this file only assembles the runtime
# layer. Alpine (not scratch) on purpose: the local bundle's bootstrap user
# Job pipes a generated password into `skalid user create` through a shell.
# Dev and e2e builds keep using build/skalid.Dockerfile.
FROM alpine:3.21
RUN apk add --no-cache ca-certificates
COPY skalid /usr/local/bin/skalid
EXPOSE 7070
ENTRYPOINT ["skalid"]
