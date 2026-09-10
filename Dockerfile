# ==============================================================================
# Stage 1: Build binary statically using Go Alpine
# ==============================================================================
FROM golang:1.23-alpine AS builder

WORKDIR /build

# Cài đặt git để fetch Go modules nếu cần
RUN apk add --no-cache git ca-certificates

COPY go.mod go.sum* ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -ldflags="-s -w -extldflags '-static'" \
    -o /build/server ./cmd/server

# ==============================================================================
# Stage 2: Minimal Runtime Container (Tối ưu RAM < 40MB, size < 30MB)
# ==============================================================================
FROM alpine:3.20

RUN apk --no-cache add ca-certificates tzdata && \
    addgroup -g 10001 appgroup && \
    adduser -u 10001 -G appgroup -s /bin/sh -D appuser

WORKDIR /app

# Copy binary và templates
COPY --from=builder /build/server /app/server
COPY --from=builder /build/web /app/web

RUN chown -R appuser:appgroup /app

USER appuser

EXPOSE 8080

CMD ["/app/server"]
