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
BLUE='\033[0;34m'
NC='\033[0m'

# 检查 Root 权限
if [ "$(id -u)" != "0" ]; then
    echo -e "${RED}错误: 请使用 sudo 或 root 权限运行此脚本${NC}"
    exit 1
fi

# --- 系统检测 ---
check_os() {
    if [ -f /etc/os-release ]; then
        . /etc/os-release
        OS=$ID
    else
        OS="unknown"
    fi
}

# --- 辅助函数：安装依赖 ---
install_deps() {
    if ! command -v curl >/dev/null 2>&1; then
        echo -e "${YELLOW}正在安装 curl...${NC}"
        if [ "$OS" = "alpine" ]; then
            apk add --no-cache curl
        elif [ "$OS" = "debian" ] || [ "$OS" = "ubuntu" ]; then
            apt-get update && apt-get install -y curl
        fi
    fi
}

# --- 核心逻辑：安装 ---
do_install() {
    install_deps

    # 1. 停止旧服务（如果存在），防止文件占用
    if command -v systemctl >/dev/null 2>&1; then
        systemctl stop "$SERVICE_NAME" >/dev/null 2>&1
    elif command -v rc-service >/dev/null 2>&1; then
        rc-service "$SERVICE_NAME" stop >/dev/null 2>&1
    fi

    # 2. 下载文件
    echo -e "${YELLOW}正在下载 monitor...${NC}"
    curl -L -o "$BIN_PATH" "$DOWNLOAD_URL"
    if [ $? -ne 0 ]; then
        echo -e "${RED}下载失败，请检查网络。${NC}"
        exit 1
    fi
    chmod +x "$BIN_PATH"
    echo -e "${GREEN}下载并授权成功。${NC}"

    # 3. 配置服务
    if [ -f /run/systemd/system ] || [ "$OS" = "debian" ] || [ "$OS" = "ubuntu" ]; then
        # Systemd 安装
        echo -e "${YELLOW}配置 Systemd 服务...${NC}"
        cat > "/etc/systemd/system/${SERVICE_NAME}.service" <<EOF
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
        systemctl enable "$SERVICE_NAME"
        systemctl start "$SERVICE_NAME"
        echo -e "${GREEN}安装完成！服务已启动 (Systemd)。${NC}"

    elif [ -f /sbin/openrc-run ] || [ "$OS" = "alpine" ]; then
        # OpenRC 安装
        echo -e "${YELLOW}配置 OpenRC 服务...${NC}"
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
        rc-update add "$SERVICE_NAME" default
        rc-service "$SERVICE_NAME" start
        echo -e "${GREEN}安装完成！服务已启动 (OpenRC)。${NC}"
    else
        echo -e "${RED}无法识别服务管理器，仅下载了文件。${NC}"
    fi
}

# --- 核心逻辑：卸载 ---
do_uninstall() {
    echo -e "${YELLOW}正在卸载...${NC}"

    # 1. 停止并移除服务
    if [ -f "/etc/systemd/system/${SERVICE_NAME}.service" ]; then
        systemctl stop "$SERVICE_NAME"
        systemctl disable "$SERVICE_NAME"
        rm "/etc/systemd/system/${SERVICE_NAME}.service"
        systemctl daemon-reload
        echo -e "已移除 Systemd 服务。"
    elif [ -f "/etc/init.d/${SERVICE_NAME}" ]; then
        rc-service "$SERVICE_NAME" stop
        rc-update del "$SERVICE_NAME" default
        rm "/etc/init.d/${SERVICE_NAME}"
        echo -e "已移除 OpenRC 服务。"
    else
        echo -e "未检测到已安装的服务文件，跳过服务清理。"
    fi

    # 2. 删除二进制文件
    if [ -f "$BIN_PATH" ]; then
        rm "$BIN_PATH"
        echo -e "已删除文件: $BIN_PATH"
    fi

    echo -e "${GREEN}卸载完成。${NC}"
}

# --- 菜单界面 ---
check_os
clear
echo -e "${BLUE}=====================================${NC}"
echo -e "   Monitor Server 管理脚本"
echo -e "   系统: $OS | 路径: $CURRENT_DIR"
echo -e "${BLUE}=====================================${NC}"
echo -e "1. 安装 / 更新 (Install/Update)"
echo -e "2. 卸载 (Uninstall)"
echo -e "0. 退出 (Exit)"
echo -e "${BLUE}=====================================${NC}"

# 兼容 sh 的读取输入方式
printf "请输入数字 [1-2]: "
read choice

case "$choice" in
    1)
        do_install
        ;;
    2)
        do_uninstall
        ;;
    0)
        exit 0
        ;;
    *)
        echo -e "${RED}无效输入，退出。${NC}"
        exit 1
        ;;
esac
