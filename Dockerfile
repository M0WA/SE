FROM golang:1.25-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/searchengine-search ./cmd/search && \
    CGO_ENABLED=0 go build -o /out/searchengine-admin ./cmd/admin && \
    CGO_ENABLED=0 go build -o /out/searchengine-crawl ./cmd/crawl

FROM gcr.io/distroless/static-debian12
COPY --from=build /out/searchengine-search /usr/bin/searchengine-search
COPY --from=build /out/searchengine-admin /usr/bin/searchengine-admin
COPY --from=build /out/searchengine-crawl /usr/bin/searchengine-crawl
EXPOSE 8080
# NOTE: a crawl-server run from this image can never use chromium/firefox
# rendering (internal/adapters/browserfetcher) -- that needs a real
# Node.js runtime plus a full browser and its shared library dependencies
# (X11, NSS, fonts, ...), none of which a distroless static base can hold.
# Rendering-enabled crawls need the .deb/systemd deployment instead (see
# packaging/), or a non-distroless image built specifically for it.
# One image, three binaries -- run the same way as the systemd services
# (packaging/searchengine-*.service): pick the process via --entrypoint,
# e.g. `docker run --entrypoint /usr/bin/searchengine-admin ...`. Defaults
# to search-server, the only one meant to be reachable publicly.
ENTRYPOINT ["/usr/bin/searchengine-search"]
