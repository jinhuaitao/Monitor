# === 构建阶段 ===
FROM golang:1.23-alpine AS builder

WORKDIR /build

# 复制源码
COPY . .

# 依赖链已全部为纯 Go（SQLite 使用 glebarez/sqlite），无需 gcc / musl-dev，
# 关闭 CGO 后产出静态二进制，任何 Linux 发行版都能直接运行。
ENV CGO_ENABLED=0

# 可选：构建时注入版本号，docker build --build-arg VERSION=xxx
ARG VERSION=dev
ARG COMMIT=unknown

# 下载依赖并生成 go.sum
RUN go mod tidy

RUN go build -trimpath \
      -ldflags="-s -w -X main.BuildVersion=${VERSION} -X main.BuildCommit=${COMMIT}" \
      -o monitor . \
    && ./monitor -mode version

# === 运行阶段 ===
FROM alpine:latest

# 安装基础依赖（静态二进制不再需要 libc6-compat）
RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app

# 从构建阶段复制
COPY --from=builder /build/monitor /usr/local/bin/monitor_bin

# 创建启动脚本
RUN printf '%s\n' \
      '#!/bin/sh' \
      'cp -f /usr/local/bin/monitor_bin /app/monitor' \
      'chmod +x /app/monitor' \
      'exec /app/monitor "$@"' \
      > /start.sh && chmod +x /start.sh

# 挂载数据目录
VOLUME /app

EXPOSE 8080

ENTRYPOINT ["/start.sh"]
CMD ["-mode", "server", "-port", "8080"]
