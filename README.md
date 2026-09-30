# ⚡ Hub Monitor

Hub Monitor 是一款基于 Go + Gin + GORM 开发的轻量级、高性能服务器监控系统。专为极简主义者设计,无需复杂的环境依赖,单文件即可运行。

集成了实时状态监控、网络延迟 Ping 检测、Telegram/Webhook 告警以及现代化的“毛玻璃”UI 设计,助您轻松掌控所有基础设施。

## ✨ 核心特性

- **轻量级架构**:基于 Go 语言编写,编译后仅需一个二进制文件,内存占用极低。
- **现代化 UI**:内置 Glassmorphism(毛玻璃)风格仪表盘,支持浅色/深色模式自动切换;浏览器可“添加到主屏幕”以 PWA 独立窗口运行。
- **实时监控**
  - 系统资源:CPU、内存、硬盘使用率
  - 网络流量:实时上传/下载速率、总流量统计
  - 连通性:支持自定义目标(ICMP Ping / TCP Ping)检测网络延迟
- **一键接入**:服务端自动生成 Agent 安装命令,支持 Linux(Systemd)和 Alpine(OpenRC)一键安装/卸载。
- **灵活告警**:支持 Telegram Bot 与通用 Webhook(钉钉、飞书、Discord、Slack 等);覆盖节点离线/恢复与 CPU、内存、磁盘阈值,支持冷却窗口、维护模式与告警静音,全部告警落库可回溯。
- **运维安全**:管理员操作审计日志、登录失败按来源 IP 限流、bcrypt 密码存储、会话版本控制。
- **零依赖**:默认使用 SQLite 数据库,无需安装 MySQL 或 Redis,开箱即用。
- **在线更新**:面板自更新与客户端批量更新下发,下载全程 SHA256 校验 + ELF 架构体检。
- **个性化**:支持自定义背景图片、Bing 每日壁纸、卡片透明度及模糊度调节。

## 🚀 快速部署

### 方式一:一键脚本(推荐)

```bash
curl -o install.sh https://raw.githubusercontent.com/jinhuaitao/Monitor/master/install.sh \
  && chmod +x install.sh && sudo ./install.sh
```

脚本会自动识别 Systemd / OpenRC,完成二进制下载、SHA256 校验、服务注册与开机自启;并支持卸载、启停、状态查看等菜单操作。

### 方式二:Docker

```bash
git clone https://github.com/jinhuaitao/Monitor.git && cd Monitor
docker build --build-arg VERSION=$(git describe --tags --always) -t hub-monitor .
docker run -d --name monitor \
  -p 8080:8080 \
  -v monitor-data:/app \
  --restart unless-stopped \
  hub-monitor
```

> 数据(含 SQLite 数据库)持久化在 `/app` 卷中;容器内以非 root 用户运行,并内置 `/healthz` 健康检查。

首次访问 `http://<服务器IP>:8080` 会进入初始化页面,创建管理员账号即可使用。

## 🖥️ 客户端接入(Agent)

无需手动编译 Agent,面板内置一键安装:

1. 登录 Hub Monitor 面板;
2. 点击右上角 **⚙️ 系统管理**;
3. 进入 **➕ 添加节点** 选项卡;
4. 输入节点名称(例如:香港-阿里云),点击 **生成并复制命令**;
5. 在被监控的 VPS 上粘贴并运行该命令。

安装命令会自动探测目标机架构(amd64 / arm64)并下载对应二进制,自动适配 Systemd(Debian/Ubuntu/CentOS)或 OpenRC(Alpine)配置开机自启。

## ⚙️ 常见问题与配置

### 1. 如何配置告警?

进入 **系统管理 → 🔔 告警通知**:

- **Telegram**:填写 Bot Token 和 Chat ID;
- **Webhook**:填写钉钉/飞书/Discord/Slack 的 Webhook 地址并选择对应消息格式;
- 在 **告警规则** 中可配置离线判定时长、阈值(CPU/内存/磁盘)、重复提醒冷却,并可查看、导出与清理告警历史;
- 配置完成后点击“发送测试”验证通道连通性。

### 2. 如何修改主题?

- 点击右上角 ⚙️ 图标切换日间/夜间模式;
- 进入 **系统管理 → 🎨 外观设置**:背景(默认渐变 / Bing 每日壁纸 / 自定义图片 URL)、卡片透明度、模糊度与内边距均可调节。

### 3. 数据存在哪里?

所有数据存储在运行目录下的 `monitor.db`(SQLite)文件中,备份该文件即可。面板也提供 **系统管理 → 🗄️ 数据管理**:在线一致性备份(VACUUM INTO + 自动校验)、CSV/JSON 导出与保留策略清理。

### 4. 如何升级?

进入 **系统管理 → 🔄 版本更新**:

- **面板自更新**:下载新版二进制,SHA256 与 ELF 校验通过后替换自身并重启(Docker 环境请改用 `docker compose pull && up -d`);
- **客户端更新**:先“同步最新版本”缓存各架构二进制,再向单个或全部节点一键下发更新指令。

## 🛠️ 开发构建

```bash
go build -trimpath \
  -ldflags "-s -w -X main.BuildVersion=v1.x.x -X main.BuildCommit=$(git rev-parse --short HEAD) -X main.BuildTime=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  -o monitor .
```

## 📄 License

开源免费,按原样提供。
