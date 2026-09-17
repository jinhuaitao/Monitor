#!/bin/sh

# =================配置区域=================
# 基础下载地址前缀（使用 releases/latest 自动获取最新版本）
GITHUB_REPO="jinhuaitao/Monitor"
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

# --- 架构检测与下载地址生成 ---
get_download_url() {
    ARCH=$(uname -m)
    case "$ARCH" in
        x86_64|amd64)
            ASSET_NAME="monitor-linux-amd64"
            ;;
        aarch64|arm64)
            ASSET_NAME="monitor-linux-arm64"
            ;;
        *)
            echo -e "${RED}错误: 不支持的 CPU 架构: $ARCH${NC}"
            exit 1
            ;;
    esac
    
    # 拼接 GitHub Latest 稳定下载直链
    echo "https://github.com/${GITHUB_REPO}/releases/latest/download/${ASSET_NAME}"
}

# --- 辅助函数：检测服务管理器 ---
# 返回 1 为 Systemd, 2 为 OpenRC, 0 为未知
get_init_system() {
    if [ -f /run/systemd/system ] || [ "$OS" = "debian" ] || [ "$OS" = "ubuntu" ]; then
        return 1
    elif [ -f /sbin/openrc-run ] || [ "$OS" = "alpine" ]; then
        return 2
    else
        return 0
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

# --- 功能 1: 安装 ---
do_install() {
    install_deps
    
    # 动态获取架构对应的下载链接
    DOWNLOAD_URL=$(get_download_url)
    echo -e "${BLUE}检测到架构，下载地址: $DOWNLOAD_URL${NC}"

    # 停止旧服务
    do_stop >/dev/null 2>&1

    echo -e "${YELLOW}正在下载 monitor...${NC}"
    curl -L -o "$BIN_PATH" "$DOWNLOAD_URL"
    if [ $? -ne 0 ]; then
        echo -e "${RED}下载失败，请检查网络或确认该架构的资源是否存在。${NC}"
        exit 1
    fi
    chmod +x "$BIN_PATH"
    echo -e "${GREEN}下载并授权成功。${NC}"

    get_init_system
    INIT_SYS=$?

    if [ $INIT_SYS -eq 1 ]; then
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

    elif [ $INIT_SYS -eq 2 ]; then
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

# --- 功能 2: 卸载 ---
do_uninstall() {
    echo -e "${YELLOW}正在卸载...${NC}"
    do_stop
    
    get_init_system
    INIT_SYS=$?

    if [ $INIT_SYS -eq 1 ] && [ -f "/etc/systemd/system/${SERVICE_NAME}.service" ]; then
        systemctl disable "$SERVICE_NAME"
        rm "/etc/systemd/system/${SERVICE_NAME}.service"
        systemctl daemon-reload
        echo -e "已移除 Systemd 服务配置。"
    elif [ $INIT_SYS -eq 2 ] && [ -f "/etc/init.d/${SERVICE_NAME}" ]; then
        rc-update del "$SERVICE_NAME" default
        rm "/etc/init.d/${SERVICE_NAME}"
        echo -e "已移除 OpenRC 服务配置。"
    fi

    if [ -f "$BIN_PATH" ]; then
        rm "$BIN_PATH"
        echo -e "已删除文件: $BIN_PATH"
    fi
    echo -e "${GREEN}卸载完成。${NC}"
}

# --- 功能 3: 启动 ---
do_start() {
    echo -e "${YELLOW}正在启动服务...${NC}"
    get_init_system
    INIT_SYS=$?
    
    if [ $INIT_SYS -eq 1 ]; then
        systemctl start "$SERVICE_NAME"
    elif [ $INIT_SYS -eq 2 ]; then
        rc-service "$SERVICE_NAME" start
    else
        echo -e "${RED}未知的系统类型，无法启动。${NC}"
        return
    fi
    echo -e "${GREEN}操作完成。${NC}"
}

# --- 功能 4: 停止 ---
do_stop() {
    echo -e "${YELLOW}正在停止服务...${NC}"
    get_init_system
    INIT_SYS=$?
    
    if [ $INIT_SYS -eq 1 ]; then
        systemctl stop "$SERVICE_NAME"
    elif [ $INIT_SYS -eq 2 ]; then
        rc-service "$SERVICE_NAME" stop
    fi
    echo -e "${GREEN}操作完成。${NC}"
}

# --- 功能 5: 重启 ---
do_restart() {
    echo -e "${YELLOW}正在重启服务...${NC}"
    do_stop
    sleep 1
    do_start
}

# --- 功能 6: 状态 ---
do_status() {
    echo -e "${BLUE}>>> 服务运行状态:${NC}"
    get_init_system
    INIT_SYS=$?
    
    if [ $INIT_SYS -eq 1 ]; then
        systemctl status "$SERVICE_NAME" --no-pager
    elif [ $INIT_SYS -eq 2 ]; then
        rc-service "$SERVICE_NAME" status
    fi
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
echo -e "-------------------------------------"
echo -e "3. 启动服务 (Start)"
echo -e "4. 停止服务 (Stop)"
echo -e "5. 重启服务 (Restart)"
echo -e "6. 查看状态 (Status)"
echo -e "-------------------------------------"
echo -e "0. 退出 (Exit)"
echo -e "${BLUE}=====================================${NC}"

printf "请输入数字 [0-6]: "
read choice

case "$choice" in
    1) do_install ;;
    2) do_uninstall ;;
    3) do_start ;;
    4) do_stop ;;
    5) do_restart ;;
    6) do_status ;;
    0) exit 0 ;;
    *) echo -e "${RED}无效输入，退出。${NC}"; exit 1 ;;
esac
