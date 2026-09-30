# ======================= 构建阶段 =======================
# 与 .github/workflows/build.yml 使用同一个 Go 大版本，
# 避免「CI 编出来的二进制」与「docker build 编出来的二进制」行为不一致。
# 需要降级时可 --build-arg GO_VERSION=1.23（go.mod 的 go 指令就是 1.23）。
ARG GO_VERSION=1.27
FROM golang:${GO_VERSION}-alpine AS builder

WORKDIR /build

# 依赖链已全部为纯 Go（SQLite 用 glebarez/sqlite），无需 gcc / musl-dev。
# 关闭 CGO 后产出静态二进制，Alpine(musl) 与 Debian/CentOS(glibc) 都能直接运行。
ENV CGO_ENABLED=0
# 不允许自动下载其它 Go 工具链：一旦 go.mod 声明的版本高于基础镜像，
# 我们宁可构建当场失败，也不要一个「悄悄换了个编译器」的产物。
ENV GOTOOLCHAIN=local

# 先只复制依赖清单再下载依赖。
# 这样只要 go.mod / go.sum 没变，改任何 .go 文件都不会让依赖层缓存失效 ——
# 原来的写法是 `COPY . .` 之后 `go mod tidy`，等于每改一行代码都要重下一遍全部依赖。
COPY go.mod go.sum ./
RUN go mod download

# 再复制源码。.dockerignore 已把 monitor.db / agents/ / dist/ 等排除在外，
# 避免把运行时数据（含口令哈希与节点信息）打进镜像层。
COPY . .

# 构建时注入版本信息：docker build --build-arg VERSION=xxx --build-arg COMMIT=yyy
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_TIME=unknown

# -mod=readonly：go.sum 不完整时直接失败，而不是在构建过程中悄悄改写依赖文件。
RUN go build -trimpath -mod=readonly \
      -ldflags="-s -w \
        -X main.BuildVersion=${VERSION} \
        -X main.BuildCommit=${COMMIT} \
        -X main.BuildTime=${BUILD_TIME}" \
      -o monitor . \
    && ./monitor -mode version

# ======================= 运行阶段 =======================
FROM alpine:latest

# ca-certificates：面板要访问 GitHub API / ip-api.com / Telegram / Webhook，没有根证书会全部握手失败
# tzdata：告警与审计时间戳按本地时区展示
RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app

COPY --from=builder /build/monitor /usr/local/bin/monitor_bin

# 启动时把二进制复制一份到 /app/monitor。
#
# 看起来多余，其实是必需的：面板的「添加节点」命令会让被监控机器
# 去 GET /api/download?arch=xxx，而这个接口在面板尚未缓存对应架构的
# Agent 二进制时，会回落到分发「面板自身进程所在的 ./monitor」。
# 直接用 /usr/local/bin 里的那份的话，容器里就不存在这个文件。
RUN printf '%s\n' \
      '#!/bin/sh' \
      'cp -f /usr/local/bin/monitor_bin /app/monitor' \
      'chmod +x /app/monitor' \
      'exec /app/monitor "$@"' \
      > /start.sh && chmod +x /start.sh

# 数据目录：monitor.db（含 WAL/SHM）与 agents/ 都落在这里，请务必挂载出来
VOLUME /app

EXPOSE 8080

# 健康检查打 /healthz（面板自带，不查库、不鉴权，只回一个 ok）。
# 注意：如果通过 CMD 改了监听端口，这里的端口也要一起改，
# 否则容器会被标记为 unhealthy（不影响运行，但会让编排系统误判）。
HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 \
  CMD wget -q -O /dev/null http://127.0.0.1:8080/healthz || exit 1

ENTRYPOINT ["/start.sh"]
CMD ["-mode", "server", "-port", "8080"]
