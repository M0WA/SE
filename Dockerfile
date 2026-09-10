FROM golang:1.22-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/searchengine ./cmd/server

FROM gcr.io/distroless/static-debian12
COPY --from=build /out/searchengine /usr/bin/searchengine
EXPOSE 8080
ENTRYPOINT ["/usr/bin/searchengine"]
