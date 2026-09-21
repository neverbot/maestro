FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build \
      -ldflags "-s -w -X github.com/neverbot/maestro/internal/version.Version=${VERSION}" \
      -o /out/maestro ./cmd/maestro

# **Alpine, where this was distroless, and the reason is `pg_dump`.**
# internal/backup shells out to it for the nightly dump, and a
# distroless image has no binaries at all — so the knob would have
# existed in the configuration and been impossible to turn on in the
# image this project ships, which is the shape of lie this repository
# refuses elsewhere. The cost is honest and small: alpine plus the
# Postgres client against a static binary on scratch-with-certs.
#
# postgresql17-client, not 16: pg_dump refuses to dump a server newer
# than itself, and the client is backward compatible with older servers,
# so this one dumps a Postgres 16 and a Postgres 17 alike.
#
# **UID *and* GID are pinned to 65532.** Files the container writes into
# a mounted volume carry those numbers, and a host granting a backup
# agent access by group needs them stable across image rebuilds — an
# unpinned `addgroup -S` takes whatever number alpine has free that day.
FROM alpine:3.21
# **tzdata, because the backup hour is a local one and there was no
# "local".** Alpine ships no zone database, so Go's time.Local is UTC
# whatever `TZ` says: an operator who asked for 03:00 and set
# TZ=Europe/Madrid would get the dump at 01:00 their time, silently.
# Measured on the real image rather than assumed — the first end-to-end
# run of this feature never fired at the minute it was told to.
RUN apk add --no-cache postgresql17-client ca-certificates tzdata \
    && addgroup -S -g 65532 nonroot \
    && adduser -S -G nonroot -u 65532 -h /home/nonroot nonroot
# The label is what makes the leftovers findable. Every `make dev`
# rebuild leaves the previous image untagged, and an untagged image has
# no repository name to filter on — fifteen rebuilds in a day is most of
# a gigabyte of layers nothing can name. `make dev` prunes by this label,
# so the cleanup touches this project's images and nobody else's.
LABEL org.opencontainers.image.title="maestro"
COPY --from=build /out/maestro /maestro
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/maestro"]
