# ---- Build Stage ----
FROM golang:1.25-alpine AS builder

RUN apk add --no-cache git ca-certificates

WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /app ./cmd/analyzer

# ---- Runtime Stage ----
FROM alpine:3.19

RUN apk add --no-cache ca-certificates

# Create non-root user
RUN adduser -D -u 1000 healer
USER healer
WORKDIR /home/healer

COPY --from=builder /app /usr/local/bin/app

# Health check marker
RUN touch /tmp/healthy

ENTRYPOINT ["app"]