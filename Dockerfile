# Multi-stage build of the toykv server binary. Used by deploy/cluster (M23)
# to stand up a local 3-node replicated cluster. Standalone use is fine too:
#   docker build -t toykv . && docker run --rm toykv -addr 0.0.0.0:6390 -protected-mode no
FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/toykv ./cmd/toykv

FROM gcr.io/distroless/static-debian12
COPY --from=build /out/toykv /toykv
ENTRYPOINT ["/toykv"]
