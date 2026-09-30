# === 构建阶段 ===
FROM golang:1.23-alpine AS builder

WORKDIR /build

# 依赖链已全部为纯 Go（SQLite 使用 glebarez/sqlite），无需 gcc / musl-dev，
# 关闭 CGO 后产出静态二进制，任何 Linux 发行版都能直接运行。
ENV CGO_ENABLED=0

# 先只复制依赖清单再下载：源码改动不会让依赖下载层失效（构建缓存友好）。
# 用 go mod download 而非 go mod tidy：tidy 会改写 go.mod/go.sum，
# 构建容器里不该产生依赖变更 —— 清单不一致应当直接报错，而不是被静默修好。
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# 可选：构建时注入版本信息，docker build --build-arg VERSION=xxx
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_TIME=unknown

RUN go build -trimpath \
      -ldflags="-s -w -X main.BuildVersion=${VERSION} -X main.BuildCommit=${COMMIT} -X main.BuildTime=${BUILD_TIME}" \
      -o monitor . \
    && ./monitor -mode version

# === 运行阶段 ===
# 固定基础镜像版本而非 alpine:latest：保证任意时间点重建镜像行为一致，
# 也便于复现线上问题。
FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata su-exec

# 以非 root 运行：面板被攻破时，攻击者拿到的不是一个 root Shell。
# 监听 8080 与读取 /proc 指标均无需特权。
RUN adduser -D -u 1000 monitor

# 启动脚本：把二进制复制到可写的 /app（数据目录），随后降权到 monitor 运行。
# 用 su-exec 而非直接 USER：兼容宿主目录 bind mount 属主为 root 的场景 ——
# 脚本仍能以 root 修正 /app 属主，再切换身份。
RUN printf '%s\n' \
      '#!/bin/sh' \
      'mkdir -p /app' \
      'cp -f /usr/local/bin/monitor_bin /app/monitor' \
      'chmod +x /app/monitor' \
      'chown -R monitor:monitor /app' \
      'exec su-exec monitor:monitor /app/monitor "$@"' \
      > /start.sh && chmod +x /start.sh

# 挂载数据目录
VOLUME /app

EXPOSE 8080

# 健康探针走面板自带的 /healthz：编排系统据此判断是否需要重启容器
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD wget -q -T 4 -O /dev/null http://127.0.0.1:8080/healthz || exit 1

ENTRYPOINT ["/start.sh"]
CMD ["-mode", "server", "-port", "8080"]
