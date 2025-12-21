⚡ Hub Monitor
Hub Monitor 是一款基于 Go + Gin + Gorm 开发的轻量级、高性能服务器监控系统。它专为极简主义者设计，无需复杂的环境依赖，单文件即可运行。

集成了实时状态监控、网络延迟 Ping 检测、Telegram/Webhook 告警以及现代化的“毛玻璃”UI 设计，助您轻松掌控所有基础设施。

✨ 核心特性
轻量级架构：基于 Go 语言编写，编译后仅需一个二进制文件，内存占用极低。

现代化 UI：内置精美的 Glassmorphism（毛玻璃）风格仪表盘，支持浅色/深色模式自动切换。

实时监控：

系统资源：CPU、内存、硬盘使用率。

网络流量：实时上传/下载速率、总流量统计。

连通性：支持自定义目标（ICMP Ping / TCP Ping）检测网络延迟。

一键接入：服务端自动生成 Agent 安装命令，支持 Linux (Systemd) 和 Alpine (OpenRC) 一键安装/卸载。

灵活告警：支持 Telegram Bot 和通用 Webhook（钉钉、飞书、Discord 等）离线/上线通知。

零依赖：默认使用 SQLite 数据库，无需安装 MySQL 或 Redis，开箱即用。

个性化：支持自定义背景图片、Bing 每日壁纸、卡片透明度及模糊度调节。

🚀 部署教程
由于项目是单文件 Go 程序，部署非常简单。

一、 环境准备
服务器：一台拥有公网 IP 的 VPS（作为服务端）。

# 方式一：一键脚本
```
curl -o install.sh https://raw.githubusercontent.com/jinhuaitao/Monitor/master/install.sh && chmod +x install.sh && ./install.sh
```
# 方式二：使用 Docker Compose（推荐）
这种方式最易于管理和升级。

### 1.创建一个文件夹（例如 monitor），进入该文件夹。

创建 docker-compose.yml 文件，内容如下：

```

version: '3.8'

services:
  hub-monitor:
    image: jhtone/hubmonitor:latest
    container_name: hub-monitor
    restart: always
    ports:
      - "8080:8080"
    volumes:
      # 挂载当前目录下的 data 文件夹到容器内的 /app
      # 这里会保存 monitor.db 和下载用的二进制文件
      - ./data:/app
    environment:
      # 设置时区，保证日志和监控时间正确
      - TZ=Asia/Shanghai
```
### 启动服务：
```
docker-compose up -d
```
### 2.使用 Docker 命令行 (Docker CLI)
如果您不想创建文件，直接在终端执行以下命令即可启动：
```
docker run -d --name hub-monitor --restart always -p 8080:8080 -v $(pwd)/data:/app -e TZ=Asia/Shanghai jhtone/hubmonitor:latest
```
(注意：$(pwd)/data 表示在当前目录下创建一个 data 文件夹用于挂载)

✅ 启动后检查
访问面板： 在浏览器输入 http://您的服务器IP:8080。

检查数据持久化： 查看您服务器上的挂载目录（例如 ./data），您应该能看到生成了以下文件：

monitor.db（数据库文件，请勿删除）

monitor（二进制文件，用于 Agent 节点下载）

查看日志（如果无法访问）：

Bash

docker logs -f hub-monitor
❓ 常见问题
端口冲突：如果 8080 已经被占用，修改冒号前面的端口，例如 -p 8088:8080。

Agent 无法下载：在面板添加节点时，请确保填写的“面板公网地址”是 http://您的IP:端口，否则 Agent 无法找到下载链接。
三、 客户端接入 (Agent)
无需手动编译 Agent，面板内置了一键安装功能。

登录 Hub Monitor 面板。

点击右上角的 “⚙️ 系统管理”。

进入 “➕ 添加节点” 选项卡。

输入节点名称（例如：香港-阿里云），点击 “生成并复制命令”。

在被监控的 VPS 上 粘贴并运行该命令即可。

提示：安装脚本会自动识别 Systemd (Debian/Ubuntu/CentOS) 或 OpenRC (Alpine) 并配置开机自启。

四、 常见问题 & 配置
1. 如何配置告警？
进入 系统管理 -> 🔔 告警通知：

Telegram：填写 Bot Token 和 Chat ID。

Webhook：填写钉钉/飞书的 Webhook URL。

配置完成后点击“发送测试”验证。

2. 如何修改主题？
点击右上角的 ⚙️ 图标可以切换日间/夜间模式。 进入 系统管理 -> 🎨 外观设置，可以设置：

背景：默认渐变、Bing 每日壁纸、或自定义图片 URL。

透明度：调整卡片毛玻璃效果的强度。

3. 数据存在哪里？
所有数据存储在运行目录下的 monitor.db (SQLite) 文件中。备份时只需备份此文件即可。
