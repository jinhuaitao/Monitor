# ⚡ Hub Monitor

基于 **Go + Gin + GORM + SQLite** 的轻量级服务器监控面板。单二进制、零外部依赖，编译产物只有一个文件，内存占用极低。

集成实时状态监控、网络延迟 Ping 检测、Telegram / Webhook 告警，以及毛玻璃风格的响应式 UI（含 PWA 离线支持）。

---

## ✨ 核心特性

- **轻量级架构**：单个二进制文件，无 MySQL / Redis 依赖，开箱即用。
- **现代化 UI**：Glassmorphism 毛玻璃风格，浅色 / 深色自动切换，支持自定义背景与透明度。
- **实时监控**
  - 系统资源：CPU、内存、硬盘使用率
  - 网络流量：实时上传 / 下载速率、累计流量
  - 连通性：自定义 ICMP / TCP Ping 目标，逐目标延迟
- **一键接入**：面板生成安装命令，自动识别 Systemd（Debian / Ubuntu / CentOS）或 OpenRC（Alpine）并配置开机自启。
- **灵活告警**：Telegram Bot 与通用 Webhook（钉钉 / 飞书 / Discord 等），支持离线、恢复、CPU / 内存 / 磁盘阈值。
- **PWA**：可安装到桌面，断网时回退离线页并自动探测面板恢复。
- **自更新**：面板与 Agent 均可在界面内一键升级，失败自动回滚。

---

## 🚀 部署

### 方式一：一键脚本（推荐）

```sh
curl -o install.sh https://raw.githubusercontent.com/jinhuaitao/Monitor/master/install.sh
chmod +x install.sh && ./install.sh
```

脚本会下载对应架构的二进制、校验完整性（SHA-256 + ELF 架构双重校验）、安装为系统服务并等待进程就绪。

### 方式二：Docker

```sh
docker build -t hub-monitor .
docker run -d --name hub-monitor -p 8080:8080 \
  -v "$PWD/data:/data" hub-monitor
```

容器内以非 root 用户运行，数据库位于 `/data/monitor.db`。内置 `HEALTHCHECK` 打 `/healthz`。

### 方式三：自行编译

```sh
make build        # 产出 ./monitor
./monitor -port 8080
```

---

## ⚙️ 环境变量

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `TRUSTED_PROXIES` | `127.0.0.0/8,::1/128` | 可信反向代理网段，逗号分隔，支持 CIDR 或裸 IP。**仅当请求来自这些地址时才会解析 `X-Forwarded-For`。** |
| `COOKIE_SECURE` | 未设置 | 设为 `1` 时给会话 Cookie 加 `Secure` 标记。仅在 HTTPS 访问面板时开启 —— 以 `http://IP:8080` 直连时开启会导致登录后立刻掉线。 |

命令行参数：`-port`（默认 `8080`）、`-mode`（`server` / `agent` / `install`）、`-server`、`-token`、`-id`。

### 挂在 Nginx / Caddy 后面

面板默认只信任回环地址，因此**同机部署的 Nginx 无需任何额外配置**即可正确识别访客来源。若代理在另一台机器上，需显式声明：

```sh
TRUSTED_PROXIES=10.0.0.0/8,172.16.0.0/12 ./monitor -port 8080
```

Nginx 侧务必使用**追加**语义传递来源，否则真实地址会丢失：

```nginx
location / {
    proxy_pass http://127.0.0.1:8080;
    proxy_set_header Host              $host;
    proxy_set_header X-Real-IP         $remote_addr;
    proxy_set_header X-Forwarded-For   $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;
}
```

> `$proxy_add_x_forwarded_for` 会把客户端自带的 `X-Forwarded-For` 保留在最左侧再追加真实地址。面板因此**从右往左**取第一个非可信地址 —— 取最左值等于把伪造权交给请求方。

配置错误（例如写成非法 CIDR）会让面板**拒绝启动**并打印原因，而不是静默忽略。

---

## 🔐 安全说明

- 管理员口令使用 **bcrypt（cost 12）** 存储；超过 bcrypt 72 字节上限的口令会先经 SHA-256 归一化，不会静默降级为弱哈希。
- 会话签名密钥持久化在数据库中，**重启不会把所有人踢下线**；在「账号安全」页可轮换会话版本，强制所有设备重新登录。
- 登录限流**按真实来源地址**计数：对端不可信时完全忽略转发头，对端可信时才从右往左解析。连续失败 5 次锁定 10 分钟，且**锁定中的请求不会延长锁定窗口**。
- 所有管理操作写入审计日志（含来源地址），默认保留 90 天。
- 响应头默认下发 CSP、`X-Frame-Options: DENY`、`nosniff`、`Referrer-Policy`、`Permissions-Policy`、COOP；仅当检测到 TLS 时下发 HSTS。
- 面板分发的二进制与安装命令均经校验；`/api/report` 有报文体积、Ping 目标数量与自动注册节点数上限。

---

## 🖥️ 接入节点

1. 登录面板，打开右上角 **⚙️ 系统管理**。
2. 进入 **➕ 接入节点**，填写节点名称（如 `香港-阿里云`）与分组。
3. 点击 **生成并复制命令**，在被监控的 VPS 上粘贴执行。

Agent 会自动识别 Systemd / OpenRC 并配置开机自启。

---

## 💾 数据与备份

数据默认存放在**运行目录**下的 `monitor.db`（SQLite，WAL 模式）。

> ⚠️ 不要直接 `cp monitor.db`。WAL 模式下新提交的事务先写入 `monitor.db-wal`，未合并时拷出来的可能是个空库。
>
> 请使用 **系统管理 → 🗄️ 数据管理 → 下载数据库备份**，该接口会在拷贝前合并 WAL，产出单文件快照。

---

## 🛠️ 开发

```sh
make check      # gofmt 校验 + go vet + go test -race（与 CI 的 verify job 等价）
make test       # 单元测试
make test-race  # 竞态检测
make vuln       # govulncheck 依赖漏洞扫描
make run        # 本地启动
make help       # 查看全部目标
```

CI（`.github/workflows/build.yml`）在每次 push / PR 时执行：

1. `verify` —— `go mod tidy` 漂移检查、`gofmt -l`、`go vet`、`go test -race`
2. `vulncheck` —— `govulncheck`（非阻塞）
3. `build` —— 交叉编译 amd64 / arm64，并校验产物架构
4. `version` —— 生成带版本信息的 Release

---

## ❓ 常见问题

**告警怎么配？** 系统管理 → 🔔 告警通知，填写 Telegram Bot Token + Chat ID，或钉钉 / 飞书 Webhook URL，点「发送测试」验证。

**主题怎么改？** 右上角 ⚙️ 切换日间 / 夜间；系统管理 → 🎨 外观设置可调整背景（渐变 / Bing 每日壁纸 / 自定义图片 URL）与卡片透明度。

**登录后被立刻踢回登录页？** 通常是 HTTPS 场景下 `COOKIE_SECURE` 未开启，或 HTTP 场景下误开了它。按上文对照调整。

**反代后面板里所有访客都显示同一个 IP？** `TRUSTED_PROXIES` 未包含代理地址，面板按设计忽略了转发头。参考上文「挂在 Nginx / Caddy 后面」。
