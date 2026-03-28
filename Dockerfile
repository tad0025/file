# Build stage
FROM golang:1.21-alpine AS builder
WORKDIR /app
COPY . .

# Tự động tạo go.sum và tải thư viện
RUN go mod tidy 
RUN go build -o server main.go

# Run stage
FROM alpine:latest
WORKDIR /root/
COPY --from=builder /app/server .
COPY --from=builder /app/public ./public
EXPOSE 8080
CMD ["./server"]