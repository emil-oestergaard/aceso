# syntax=docker/dockerfile:1.7
# ---------- builder ----------
# Compile a fully-static binary so the final image needs nothing but ca-certificates.
FROM golang:1.26-alpine AS builder

WORKDIR /src

# Cache deps separately from sources for faster incremental rebuilds.
COPY go.mod ./
# go.sum is optional for V0 (stdlib-only) but copy if present.
COPY go.su[m] ./
RUN go mod download

COPY . .

# CGO off + static linking so the alpine final image runs the binary without glibc.
ENV CGO_ENABLED=0 GOOS=linux
RUN go build -trimpath -ldflags="-s -w" -o /out/aceso ./

# ---------- final ----------
FROM alpine:3.23

# ca-certificates so HTTPS calls to managed Prometheus/Loki/Ollama work out of the box.
# tzdata so RFC3339 timestamps render in UTC consistently.
RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S aceso && adduser -S -G aceso aceso \
    && mkdir -p /data && chown aceso:aceso /data

COPY --from=builder /out/aceso /aceso

USER aceso
WORKDIR /

# /data is where incidents.json is appended; mount a volume here in compose.
VOLUME ["/data"]

ENTRYPOINT ["/aceso"]
