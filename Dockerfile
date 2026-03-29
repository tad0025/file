# syntax=docker/dockerfile:1.7

FROM golang:1.21-alpine AS builder
WORKDIR /src

# Tận dụng cache layer cho dependencies.
COPY go.mod go.sum* ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

# Copy source sau để thay code không làm mất cache deps.
COPY main.go ./
COPY public ./public

# Build static binary, giảm size và tăng tốc startup.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -mod=mod -trimpath -ldflags="-s -w -buildid=" -o /out/server ./main.go

# Runtime siêu nhẹ, không shell, chạy non-root.
FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=builder /out/server /app/server
COPY --from=builder /src/public /app/public

ENV PORT=8080
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/app/server"]
