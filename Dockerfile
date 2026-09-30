# ============================================================================
# Hub Monitor —— 多阶段构建
#
# 目标产物：一个不含编译器、不含源码、以非 root 身份运行的镜像。
# 面板本身能「以 root 身份替换全部被控节点的二进制并重启」，所以它自己
# 不应该再以 root 跑 —— 容器逃逸或面板被拿下时，那是最后一道边界。
# ============================================================================

# === 构建阶段 ===
FROM golang:1.23-alpine AS builder

WORKDIR /build

# 依赖链已全部为纯 Go（SQLite 使用 glebarez/sqlite），无需 gcc / musl-dev，
# 关闭 CGO 后产出静态二进制，任何 Linux 发行版都能直接运行。
ENV CGO_ENABLED=0

# 先只复制依赖清单，再下载依赖。
# 原实现是 `COPY . .` 之后才 `go mod tidy`：任何一次源码改动都会让
# 「下载依赖」这一层缓存失效，改一行注释也要把全部依赖重下一遍。
COPY go.mod go.sum ./
RUN go mod download

# 再复制源码。到这里上面的依赖层已经命中缓存，只有编译需要重跑。
COPY . .

# 可选：构建时注入版本号，docker build --build-arg VERSION=xxx
ARG VERSION=dev
ARG COMMIT=unknown

# 不再用 `go mod tidy`：
#   - 它会在构建过程中改写 go.mod / go.sum，同一份源码可能编出不同的
#     依赖集合，构建结果不可复现；
#   - 它需要联网解析全部依赖，网络抖动会让构建直接失败。
# go.sum 已在仓库里，`go mod download` 就够了；Go 1.16+ 默认
# -mod=readonly，缺依赖时会明确报错而不是悄悄改清单。
RUN go build -trimpath \
      -ldflags="-s -w -X main.BuildVersion=${VERSION} -X main.BuildCommit=${COMMIT}" \
      -o monitor . \
    && ./monitor -mode version

# === 运行阶段 ===
FROM alpine:latest

# 安装基础依赖（静态二进制不再需要 libc6-compat）
# curl 供 HEALTHCHECK 使用；ca-certificates 用于访问 GitHub 检查更新
RUN apk add --no-cache ca-certificates tzdata curl

# 非 root 运行。uid 固定成 10001，方便在宿主机上对数据目录设权限。
RUN adduser -D -u 10001 -s /sbin/nologin monitor \
    && mkdir -p /app \
    && chown -R monitor:monitor /app

# 二进制放系统目录、root 拥有、全局可执行：
# 面板自身的文件必须对运行用户只读，否则一个被注入的进程就能改掉它。
COPY --from=builder --chmod=0755 /build/monitor /usr/local/bin/monitor

WORKDIR /app

# 挂载数据目录。
# 注意顺序：镜像里已经把 /app 的属主设成 monitor，Docker 用命名卷初始化
# 空目录时会继承这份属主，因此非 root 进程可以正常写 monitor.db。
# 如果换成 bind mount，宿主机上的目录需要自行 chown 10001:10001。
VOLUME /app

EXPOSE 8080

# 探针打 /healthz：它刻意不在 Service Worker 的缓存白名单里，
# 请求会真的落到服务端，因此能反映进程是否真的活着。
# 自更新脚本也用同一个端点判断新版本有没有起来。
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
  CMD curl -fsS http://127.0.0.1:8080/healthz || exit 1

# 直接 exec 二进制，不再复制到 /app：
# 面板对外分发的是「正在运行的自己」（selfPath），复制一份只会让
# 「/api/download 给出的文件」和「实际在跑的文件」变成两个东西。
USER monitor

ENTRYPOINT ["/usr/local/bin/monitor"]
CMD ["-mode", "server", "-port", "8080"]
