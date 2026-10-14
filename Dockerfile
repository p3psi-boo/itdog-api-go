# ---------- 构建阶段 ----------
FROM golang:1.22-alpine AS builder

WORKDIR /src

# 先拷贝依赖清单，利用 Docker 层缓存
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

# ---------- 运行阶段 ----------
FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S app && adduser -S app -G app

WORKDIR /app
COPY --from=builder /out/server ./server
RUN mkdir -p /app/data && chown -R app:app /app

# 以非 root 运行；数据卷（会话 cookie 持久化）
USER app
VOLUME /app/data

# 时区可覆盖：-e TZ=Asia/Shanghai
ENV HTTP_PORT=3000 \
    ITDOG_COOKIE_FILE=/app/data/cookies.json \
    GIN_MODE=release

EXPOSE 3000

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD wget -qO- "http://127.0.0.1:${HTTP_PORT}/healthz" >/dev/null 2>&1 || exit 1

ENTRYPOINT ["./server"]
