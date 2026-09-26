#!/bin/sh

# =================配置区域=================
# GitHub 仓库（owner/repo）
GITHUB_REPO="jinhuaitao/Monitor"
# 下载加速镜像前缀，国内网络可填 https://ghfast.top/ ，留空为直连
# 例如: MIRROR="https://ghfast.top/"
MIRROR=""
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
    printf '%b\n' "${RED}错误: 请使用 sudo 或 root 权限运行此脚本${NC}"
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
get_asset_name() {
    ARCH=$(uname -m)
    case "$ARCH" in
        x86_64|amd64)
            echo "monitor-linux-amd64"
            ;;
        aarch64|arm64)
            echo "monitor-linux-arm64"
            ;;
        *)
            printf '%b\n' "${RED}错误: 不支持的 CPU 架构: $ARCH${NC}" >&2
            exit 1
            ;;
    esac
}

# 拼接下载直链（自动带上镜像前缀）
get_download_url() {
    echo "${MIRROR}https://github.com/${GITHUB_REPO}/releases/latest/download/$1"
}

# --- 辅助函数：检测服务管理器 ---
# 返回 1 为 Systemd, 2 为 OpenRC, 0 为未知
#
# 注意：/run/systemd/system 是【目录】而非文件，
# 早期版本用 -f 判断会导致 CentOS / RHEL / Rocky 等被误判为"无法识别"，
# 从而只下载二进制却不注册服务。
get_init_system() {
    # 1. systemd 正在作为 init 运行（最可靠的判据）
    if [ -d /run/systemd/system ]; then
        return 1
    fi
    # 2. OpenRC 正在运行
    if [ -f /sbin/openrc-run ] || command -v rc-service >/dev/null 2>&1; then
        return 2
    fi
    # 3. 按发行版兜底
    case "$OS" in
        alpine) return 2 ;;
        debian|ubuntu|centos|rhel|rocky|almalinux|fedora|arch|opensuse*) return 1 ;;
    esac
    # 4. 最后看命令是否存在
    if command -v systemctl >/dev/null 2>&1; then
        return 1
    fi
    return 0
}

# --- 辅助函数：计算文件 SHA256（兼容 busybox / coreutils / macOS） ---
sha256_of() {
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum "$1" | awk '{print $1}'
    elif command -v shasum >/dev/null 2>&1; then
        shasum -a 256 "$1" | awk '{print $1}'
    elif command -v openssl >/dev/null 2>&1; then
        openssl dgst -sha256 "$1" | awk '{print $NF}'
    else
        echo ""
    fi
}

# --- 辅助函数：安装依赖 ---
install_deps() {
    if ! command -v curl >/dev/null 2>&1; then
        printf '%b\n' "${YELLOW}正在安装 curl...${NC}"
        if [ "$OS" = "alpine" ]; then
            apk add --no-cache curl
        elif [ "$OS" = "debian" ] || [ "$OS" = "ubuntu" ]; then
            apt-get update && apt-get install -y curl
        fi
    fi
}

# --- 辅助函数：下载并校验 ---
# 关键点：必须带 -f，否则 HTTP 404 时 curl 仍返回 0，
# 会把 "Not Found" 当成二进制装进去，表现为服务永远起不来。
download_binary() {
    ASSET_NAME="$1"
    URL=$(get_download_url "$ASSET_NAME")
    SHA_URL=$(get_download_url "${ASSET_NAME}.sha256")
    printf '%b\n' "${BLUE}下载地址: $URL${NC}"

    ATTEMPT=1
    while [ "$ATTEMPT" -le 3 ]; do
        if [ "$ATTEMPT" -gt 1 ]; then
            # 发布新版本时 GitHub 会逐个覆盖 Release 资源，需要几秒才能恢复一致
            printf '%b\n' "${YELLOW}第 ${ATTEMPT} 次重试...${NC}"
            if [ "$ATTEMPT" -eq 2 ]; then sleep 3; else sleep 8; fi
        fi

        if ! curl -fL --progress-bar --connect-timeout 20 --retry 2 -o "$BIN_PATH" "$URL"; then
            printf '%b\n' "${RED}下载失败：请检查网络，或确认该架构的资源是否存在。${NC}"
            rm -f "$BIN_PATH"
            ATTEMPT=$((ATTEMPT + 1))
            continue
        fi

        # 校验完整性（Release 中带有 .sha256 资源）
        EXPECT=$(curl -fsSL --connect-timeout 20 "$SHA_URL" 2>/dev/null | awk '{print $1}')
        if [ -z "$EXPECT" ]; then
            printf '%b\n' "${YELLOW}未获取到校验文件，跳过完整性校验。${NC}"
            chmod +x "$BIN_PATH"
            return 0
        fi

        ACTUAL=$(sha256_of "$BIN_PATH")
        if [ -z "$ACTUAL" ]; then
            printf '%b\n' "${YELLOW}系统缺少 sha256 工具，跳过完整性校验。${NC}"
            chmod +x "$BIN_PATH"
            return 0
        fi

        if [ "$EXPECT" = "$ACTUAL" ]; then
            printf '%b\n' "${GREEN}SHA256 校验通过。${NC}"
            chmod +x "$BIN_PATH"
            return 0
        fi

        # 校验不一致：打印细节，便于区分"网络改写"与"Release 正在更新"
        SIZE=$(wc -c < "$BIN_PATH" 2>/dev/null)
        printf '%b\n' "${YELLOW}校验不一致（第 ${ATTEMPT} 次）${NC}"
        printf '%b\n' "  期望: ${EXPECT}"
        printf '%b\n' "  实际: ${ACTUAL}"
        printf '%b\n' "  大小: ${SIZE} 字节"
        rm -f "$BIN_PATH"
        ATTEMPT=$((ATTEMPT + 1))
    done

    printf '%b\n' "${RED}SHA256 校验连续 3 次失败，已放弃安装。${NC}"
    printf '%b\n' "${YELLOW}常见原因：${NC}"
    printf '%b\n' "  1) 仓库正在发布新版本，GitHub 覆盖 Release 资源需数秒 —— 稍等 1 分钟后重试"
    printf '%b\n' "  2) 网络中间层改写了下载内容 —— 可在脚本顶部设置 MIRROR=\"https://ghfast.top/\" 后重试"
    return 1
}

# --- 辅助函数：等待旧进程完全退出（避免覆盖正在运行的二进制） ---
wait_stopped() {
    if ! command -v pgrep >/dev/null 2>&1; then
        sleep 1
        return 0
    fi
    i=0
    while [ "$i" -lt 10 ]; do
        if ! pgrep -f "$BIN_PATH" >/dev/null 2>&1; then
            return 0
        fi
        sleep 1
        i=$((i + 1))
    done
    return 1
}

# --- 功能 1: 安装 / 更新 ---
do_install() {
    install_deps

    # 停止旧服务
    do_stop >/dev/null 2>&1
    if ! wait_stopped; then
        printf '%b\n' "${YELLOW}警告: 旧进程似乎仍在运行，将强制结束。${NC}"
        pkill -f "$BIN_PATH" 2>/dev/null
        sleep 2
    fi

    # 备份旧版本，便于失败回滚
    if [ -f "$BIN_PATH" ]; then
        cp -f "$BIN_PATH" "$BIN_PATH.bak" 2>/dev/null || true
    fi

    if ! download_binary "$(get_asset_name)"; then
        # 失败必须回滚并重新拉起服务，否则一次失败的更新会把面板直接留在停机状态
        if [ -f "$BIN_PATH.bak" ]; then
            mv -f "$BIN_PATH.bak" "$BIN_PATH"
            chmod +x "$BIN_PATH"
            printf '%b\n' "${YELLOW}已回滚到旧版本。${NC}"
        fi
        do_start >/dev/null 2>&1
        printf '%b\n' "${GREEN}已重新拉起原服务，面板保持可用。${NC}"
        return 1
    fi
    printf '%b\n' "${GREEN}下载并授权成功。${NC}"

    get_init_system
    INIT_SYS=$?

    if [ $INIT_SYS -eq 1 ]; then
        # ================= Systemd =================
        printf '%b\n' "${YELLOW}配置 Systemd 服务...${NC}"
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
        systemctl enable "$SERVICE_NAME" >/dev/null 2>&1
        # 用 restart 而非 start：服务已在运行时也能正确加载新二进制
        systemctl restart "$SERVICE_NAME"
        printf '%b\n' "${GREEN}安装完成！服务已启动 (Systemd)。${NC}"

    elif [ $INIT_SYS -eq 2 ]; then
        # ================= OpenRC (Alpine) =================
        printf '%b\n' "${YELLOW}配置 OpenRC 服务...${NC}"
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
        rc-update add "$SERVICE_NAME" default >/dev/null 2>&1
        # 清理残留 pidfile，否则会出现 "no matching processes found" 与假启动
        rm -f "/run/${SERVICE_NAME}.pid"
        rc-service "$SERVICE_NAME" start
        printf '%b\n' "${GREEN}安装完成！服务已启动 (OpenRC)。${NC}"
    else
        printf '%b\n' "${RED}无法识别服务管理器，仅下载了文件。${NC}"
        printf '%b\n' "${YELLOW}可手动运行: ${BIN_PATH} ${APP_ARGS}${NC}"
        rm -f "$BIN_PATH.bak"
        return 1
    fi

    # 安装成功后清理备份
    rm -f "$BIN_PATH.bak"

    # 展示服务状态与当前版本，便于确认更新是否真正生效
    do_status
}

# --- 功能 2: 卸载 ---
do_uninstall() {
    printf '%b\n' "${YELLOW}正在卸载...${NC}"
    do_stop

    get_init_system
    INIT_SYS=$?

    if [ $INIT_SYS -eq 1 ] && [ -f "/etc/systemd/system/${SERVICE_NAME}.service" ]; then
        systemctl disable "$SERVICE_NAME" >/dev/null 2>&1
        rm "/etc/systemd/system/${SERVICE_NAME}.service"
        systemctl daemon-reload
        printf '%b\n' "已移除 Systemd 服务配置。"
    elif [ $INIT_SYS -eq 2 ] && [ -f "/etc/init.d/${SERVICE_NAME}" ]; then
        rc-update del "$SERVICE_NAME" default >/dev/null 2>&1
        rm "/etc/init.d/${SERVICE_NAME}"
        rm -f "/run/${SERVICE_NAME}.pid"
        printf '%b\n' "已移除 OpenRC 服务配置。"
    fi

    if [ -f "$BIN_PATH" ]; then
        rm "$BIN_PATH"
        printf '%b\n' "已删除文件: $BIN_PATH"
    fi
    printf '%b\n' "${GREEN}卸载完成。${NC}"
}

# --- 功能 3: 启动 ---
do_start() {
    printf '%b\n' "${YELLOW}正在启动服务...${NC}"
    get_init_system
    INIT_SYS=$?

    if [ $INIT_SYS -eq 1 ]; then
        systemctl start "$SERVICE_NAME"
    elif [ $INIT_SYS -eq 2 ]; then
        rc-service "$SERVICE_NAME" start
    else
        printf '%b\n' "${RED}未知的系统类型，无法启动。${NC}"
        return
    fi
    printf '%b\n' "${GREEN}操作完成。${NC}"
}

# --- 功能 4: 停止 ---
do_stop() {
    printf '%b\n' "${YELLOW}正在停止服务...${NC}"
    get_init_system
    INIT_SYS=$?

    if [ $INIT_SYS -eq 1 ]; then
        systemctl stop "$SERVICE_NAME"
    elif [ $INIT_SYS -eq 2 ]; then
        rc-service "$SERVICE_NAME" stop
    fi
    printf '%b\n' "${GREEN}操作完成。${NC}"
}

# --- 功能 5: 重启 ---
do_restart() {
    printf '%b\n' "${YELLOW}正在重启服务...${NC}"
    do_stop
    sleep 1
    do_start
}

# --- 功能 6: 状态 ---
do_status() {
    printf '%b\n' "${BLUE}>>> 服务运行状态:${NC}"
    get_init_system
    INIT_SYS=$?

    if [ $INIT_SYS -eq 1 ]; then
        systemctl status "$SERVICE_NAME" --no-pager
    elif [ $INIT_SYS -eq 2 ]; then
        rc-service "$SERVICE_NAME" status
    else
        printf '%b\n' "${YELLOW}未识别的服务管理器，请检查进程是否存活:${NC}"
        pgrep -f "$BIN_PATH" >/dev/null 2>&1 && echo "进程存活" || echo "进程未运行"
    fi

    if [ -f "$BIN_PATH" ]; then
        if command -v timeout >/dev/null 2>&1; then
            VER=$(timeout 5 "$BIN_PATH" -mode version 2>/dev/null | head -n 1)
        else
            VER=$("$BIN_PATH" -mode version 2>/dev/null | head -n 1)
        fi
        [ -n "$VER" ] && printf '%b\n' "${GREEN}${VER}${NC}"
    fi
}

# --- 菜单界面 ---
check_os
clear
printf '%b\n' "${BLUE}=====================================${NC}"
printf '%b\n' "   Monitor Server 管理脚本"
printf '%b\n' "   系统: $OS | 路径: $CURRENT_DIR"
printf '%b\n' "${BLUE}=====================================${NC}"
printf '%b\n' "1. 安装 / 更新 (Install/Update)"
printf '%b\n' "2. 卸载 (Uninstall)"
printf '%b\n' "-------------------------------------"
printf '%b\n' "3. 启动服务 (Start)"
printf '%b\n' "4. 停止服务 (Stop)"
printf '%b\n' "5. 重启服务 (Restart)"
printf '%b\n' "6. 查看状态 (Status)"
printf '%b\n' "-------------------------------------"
printf '%b\n' "0. 退出 (Exit)"
printf '%b\n' "${BLUE}=====================================${NC}"

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
    *) printf '%b\n' "${RED}无效输入，退出。${NC}"; exit 1 ;;
esac
