FROM golang:1.27-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/searchengine-search ./cmd/search && \
    CGO_ENABLED=0 go build -o /out/searchengine-admin ./cmd/admin && \
    CGO_ENABLED=0 go build -o /out/searchengine-crawl ./cmd/crawl && \
    CGO_ENABLED=0 go build -o /out/searchengine-mcp-web ./cmd/mcp-web && \
    CGO_ENABLED=0 go build -o /out/searchengine-mcp-datetime ./cmd/mcp-datetime && \
    CGO_ENABLED=0 go build -o /out/searchengine-mcp-sandbox ./cmd/mcp-sandbox && \
    CGO_ENABLED=0 go build -o /out/searchengine-mcp-files ./cmd/mcp-files && \
    CGO_ENABLED=0 go build -o /out/searchengine-mcp-vision ./cmd/mcp-vision

# The digest alone fully and unambiguously pins this image's exact
# content (it was the "nonroot" tag's digest at time of pinning, per
# gcr.io/distroless/static-debian12's own manifest -- runs as a non-root
# user either way); a tag alongside a digest is redundant and SonarCloud's
# docker:S8431 flags it.
FROM gcr.io/distroless/static-debian12@sha256:d75cdd72874d4790092fcb1b058493ecf6bb5bf2b2b897045b00ff01d91843f2
COPY --from=build /out/searchengine-search /usr/bin/searchengine-search
COPY --from=build /out/searchengine-admin /usr/bin/searchengine-admin
COPY --from=build /out/searchengine-crawl /usr/bin/searchengine-crawl
COPY --from=build /out/searchengine-mcp-web /usr/bin/searchengine-mcp-web
COPY --from=build /out/searchengine-mcp-datetime /usr/bin/searchengine-mcp-datetime
COPY --from=build /out/searchengine-mcp-sandbox /usr/bin/searchengine-mcp-sandbox
COPY --from=build /out/searchengine-mcp-files /usr/bin/searchengine-mcp-files
COPY --from=build /out/searchengine-mcp-vision /usr/bin/searchengine-mcp-vision
# This digest already IS the "nonroot" variant's content (uid/gid 65532
# baked in -- confirmed via `docker inspect --format '{{.Config.User}}'`),
# but that isn't visible from a bare digest reference alone -- an
# explicit USER here is a no-op at runtime, just makes it statically
# obvious (to SonarCloud's docker:S6471 and to a human reader) that this
# never runs as root, without depending on the tag also pinned above.
USER 65532:65532
EXPOSE 8080
# NOTE: a crawl-server run from this image can never use chromium/firefox
# rendering (internal/adapters/browserfetcher) -- that needs a real
# Node.js runtime plus a full browser and its shared library dependencies
# (X11, NSS, fonts, ...), none of which a distroless static base can hold.
# Rendering-enabled crawls need the .deb/systemd deployment instead (see
# packaging/), or a non-distroless image built specifically for it.
# One image, eight binaries -- run the same way as the systemd services
# (packaging/searchengine-*.service): pick the process via --entrypoint,
# e.g. `docker run --entrypoint /usr/bin/searchengine-admin ...`. Defaults
# to search-server, the only one meant to be reachable publicly.
# searchengine-mcp-web/searchengine-mcp-datetime/searchengine-mcp-sandbox/
# searchengine-mcp-files/searchengine-mcp-vision are never run directly --
# search-server/admin-server spawn them themselves as a stdio subprocess
# (see internal/adapters/mcpclient) whenever an admin configures an
# MCPServer row pointing at one. searchengine-mcp-sandbox specifically
# also needs a real Docker socket to spawn the sandboxed containers it
# runs code in (internal/adapters/dockersandbox) -- not available to a
# process already running inside this same distroless image without
# deliberately mounting /var/run/docker.sock through, the same
# docker-outside-of-docker caveat packaging/docker/README.md's compose
# setup calls out.
ENTRYPOINT ["/usr/bin/searchengine-search"]
