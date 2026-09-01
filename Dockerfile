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
COPY --from=build /out/maestro /maestro
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/maestro"]
