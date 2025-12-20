#!/bin/sh

# =================配置区域=================
# 下载地址
DOWNLOAD_URL="https://github.com/jinhuaitao/Monitor/releases/download/V1.0.0/monitor"
# 服务名称
SERVICE_NAME="monitor_server"
# 本地保存的文件名
BIN_NAME="monitor"
# 启动参数
APP_ARGS="-mode server -port 8080"
# ==========================================

# 获取当前目录
CURRENT_DIR=$(cd "$(dirname "$0")"; pwd)
BIN_PATH="$CURRENT_DIR/$BIN_NAME"

# 颜色定义
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[0;33m'
NC='\033[0m'

# 检查 Root 权限
if [ "$(id -u)" != "0" ]; then
    echo -e "${RED}错误: 请使用 sudo 或 root 权限运行此脚本${NC}"
    exit 1
fi

# --- 步骤 1: 环境检测与依赖安装 ---
if [ -f /etc/os-release ]; then
    . /etc/os-release
    OS=$ID
else
    OS="unknown"
fi

echo -e "${YELLOW}检测到系统: $OS${NC}"

# 安装下载工具 (curl) 如果不存在
if ! command -v curl >/dev/null 2>&1; then
    echo "未找到 curl，正在安装..."
    if [ "$OS" = "alpine" ]; then
        apk add --no-cache curl
    elif [ "$OS" = "debian" ] || [ "$OS" = "ubuntu" ]; then
        apt-get update && apt-get install -y curl
    fi
fi

# --- 步骤 2: 下载文件 ---
if [ ! -f "$BIN_PATH" ]; then
    echo -e "${YELLOW}正在从 GitHub 下载 monitor...${NC}"
    echo "地址: $DOWNLOAD_URL"
    
    # 使用 curl 下载，-L 跟随重定向 (GitHub 需要)，-o 指定输出文件名
    curl -L -o "$BIN_PATH" "$DOWNLOAD_URL"
    
    if [ $? -ne 0 ]; then
        echo -e "${RED}下载失败，请检查网络或下载地址。${NC}"
        exit 1
    fi
    echo -e "${GREEN}下载完成。${NC}"
else
    echo -e "${GREEN}文件 $BIN_NAME 已存在，跳过下载。${NC}"
fi

# 赋予执行权限
chmod +x "$BIN_PATH"

# --- 步骤 3: 安装服务 ---

# Systemd (Debian/Ubuntu)
install_systemd() {
    echo -e "${YELLOW}正在配置 Systemd 服务...${NC}"
    SERVICE_FILE="/etc/systemd/system/${SERVICE_NAME}.service"
    
    cat > "$SERVICE_FILE" <<EOF
[Unit]
Description=Monitor Server Service
After=network.target

[Service]
Type=simple
User=root
WorkingDirectory=${CURRENT_DIR}
ExecStart=${BIN_PATH} ${APP_ARGS}
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF

    systemctl daemon-reload
    systemctl enable "${SERVICE_NAME}"
    systemctl start "${SERVICE_NAME}"
    echo -e "${GREEN}Systemd 服务安装并启动成功!${NC}"
}

# OpenRC (Alpine)
install_openrc() {
    echo -e "${YELLOW}正在配置 OpenRC 服务...${NC}"
    INIT_FILE="/etc/init.d/${SERVICE_NAME}"
    
    cat > "$INIT_FILE" <<EOF
#!/sbin/openrc-run

name="${SERVICE_NAME}"
description="Monitor Server Service"
command="${BIN_PATH}"
command_args="${APP_ARGS}"
command_background=true
pidfile="/run/${SERVICE_NAME}.pid"
directory="${CURRENT_DIR}"

depend() {
    need net
    after firewall
}
EOF

    chmod +x "$INIT_FILE"
    rc-update add "${SERVICE_NAME}" default
    rc-service "${SERVICE_NAME}" start
    echo -e "${GREEN}OpenRC 服务安装并启动成功!${NC}"
}

# 根据系统类型执行安装
if [ -f /run/systemd/system ] || [ "$OS" = "debian" ] || [ "$OS" = "ubuntu" ]; then
    install_systemd
elif [ -f /sbin/openrc-run ] || [ "$OS" = "alpine" ]; then
    install_openrc
else
    # 备用检测
    if command -v systemctl >/dev/null 2>&1; then
        install_systemd
    elif command -v rc-service >/dev/null 2>&1; then
        install_openrc
    else
        echo -e "${RED}无法识别服务管理器 (非 Systemd 也非 OpenRC)，仅下载了文件。${NC}"
        echo "你可以手动运行: $BIN_PATH $APP_ARGS"
    fi
fi
