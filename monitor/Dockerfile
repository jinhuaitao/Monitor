# === 构建阶段 ===
FROM golang:1.23-alpine AS builder

# 安装构建所需的 C 编译器
RUN apk add --no-cache gcc musl-dev

WORKDIR /build

# 复制源码
COPY . .

# 下载依赖并生成 go.sum
RUN go mod tidy

# 关键修复：设置 CGO 编译参数
# -D_LARGEFILE64_SOURCE: 强制暴露 pread64/off64_t 等符号，解决 Alpine 下的编译错误
ENV CGO_CFLAGS="-D_LARGEFILE64_SOURCE"

# 编译 (添加 -tags musl 以获得更好的兼容性)
RUN CGO_ENABLED=1 go build -tags musl -ldflags="-s -w" -o monitor main.go

# === 运行阶段 ===
FROM alpine:latest

# 安装基础依赖
RUN apk add --no-cache ca-certificates tzdata libc6-compat

WORKDIR /app

# 从构建阶段复制
COPY --from=builder /build/monitor /usr/local/bin/monitor_bin

# 创建启动脚本
RUN echo '#!/bin/sh' > /start.sh && \
    echo 'cp -f /usr/local/bin/monitor_bin /app/monitor' >> /start.sh && \
    echo 'chmod +x /app/monitor' >> /start.sh && \
    echo 'exec /app/monitor "$@"' >> /start.sh && \
    chmod +x /start.sh

# 挂载数据目录
VOLUME /app

EXPOSE 8080

ENTRYPOINT ["/start.sh"]
CMD ["-mode", "server", "-port", "8080"]