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
# a mounted volume carry those numbers, and the host grants access by
# chowning the mount to them. An unpinned `addgroup -S` takes whatever
# number alpine has free that day, so a rebuild could start writing files
# under a different group — possibly one the host already uses for
# something real, which then reads as that group's name in `ls`.
#
# **Pinning the group grants nobody anything.** A dump is 0600 in a 0700
# directory, so its group bits are zero: putting a backup agent in group
# 65532 lets it read exactly nothing. This comment used to say otherwise,
# and it would have sent an operator to add an agent to a group, watch it
# fail, and then widen the mode on a file holding the whole instance.
FROM alpine:3.21
# **No tzdata package here**, and the backup hour is still local: the
# binary embeds the zone database itself (`_ "time/tzdata"` in
# cmd/maestro/main.go), which is the half of this that belongs to
# whatever reads the clock rather than to whatever base image is
# underneath it.
RUN apk add --no-cache postgresql17-client ca-certificates \
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
