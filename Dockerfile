FROM golang:1.25.7-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build \
      -ldflags "-s -w -X github.com/neverbot/maestro/internal/version.Version=${VERSION}" \
      -o /out/maestro ./cmd/maestro

FROM gcr.io/distroless/static-debian12:nonroot
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
