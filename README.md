# ⚡ Hub Monitor

轻量级服务器监控面板。Go + Gin + GORM 编写，**编译后只有一个二进制文件**，
默认使用 SQLite，无需 MySQL / Redis / Nginx，开箱即用。

集成了实时资源监控、网络延迟探测、Telegram / Webhook 告警、PWA（可安装到主屏幕）
以及面板与客户端的在线自更新，内置毛玻璃风格仪表盘，支持浅色 / 深色模式。

---

## 目录

- [核心特性](#核心特性)
- [架构说明](#架构说明)
- [快速开始](#快速开始)
  - [方式一：一键脚本](#方式一一键脚本推荐)
  - [方式二：Docker](#方式二docker)
  - [方式三：手动部署](#方式三手动部署)
- [首次配置](#首次配置)
- [接入被监控节点](#接入被监控节点)
- [配置参考](#配置参考)
  - [命令行参数](#命令行参数)
  - [环境变量](#环境变量)
  - [面板内可调项](#面板内可调项)
- [告警](#告警)
- [版本更新](#版本更新)
- [数据与备份](#数据与备份)
- [安全模型](#安全模型)
- [HTTP 接口](#http-接口)
- [开发](#开发)
- [常见问题](#常见问题)

---

## 核心特性

| 能力 | 说明 |
| --- | --- |
| **零依赖部署** | 单个静态二进制（`CGO_ENABLED=0`），Alpine(musl) 与 Debian/CentOS(glibc) 通用 |
| **资源监控** | CPU / 内存 / 磁盘使用率、实时上下行速率、累计流量、运行时长 |
| **硬件规格** | CPU 型号与核数、物理内存总量、根分区容量（采集失败时显示「—」而不是假数字） |
| **连通性探测** | 自定义目标，支持 ICMP Ping（`8.8.8.8`）与 TCP Ping（`8.8.8.8:53`） |
| **灵活告警** | 离线 / 恢复、CPU / 内存 / 磁盘阈值；Telegram + Webhook（钉钉 / 飞书 / Discord / Slack / 通用） |
| **告警历史** | 告警事件落库，可查、可筛选、可导出 —— 事后能回答「昨晚到底报过什么」 |
| **节点管理** | 分组、维护模式、告警静音、批量操作、排序与隐藏 ID |
| **在线自更新** | 面板与客户端均可一键升级，下载后做 ELF 头 + 架构 + SHA256 三重校验 |
| **安全审计** | 关键操作（改 Token / 改更新源 / 下发更新 / 改口令）全部留痕 |
| **登录防护** | 按来源 IP 的失败锁定 + 口令强度校验 + bcrypt 存储 + 会话版本失效 |
| **PWA** | 可安装到主屏幕、独立窗口运行、断网时给出离线页并自动重连 |
| **外观定制** | 默认渐变 / Bing 每日壁纸 / 自定义图片；毛玻璃模糊度、卡片透明度与内边距可调 |

---

## 架构说明

同一个二进制包含三种角色，通过 `-mode` 切换：

```
                        ┌──────────────────────────────┐
                        │   面板 (monitor -mode server) │
                        │  Gin + SQLite + 内嵌 Web UI   │
                        └───────┬──────────────┬───────┘
                                │              │
              POST /api/report  │              │  GET /api/download?arch=
              （每 5 秒心跳）    │              │  （下发 Agent 二进制）
                                │              │
                  ┌─────────────▼───┐   ┌──────▼─────────────────┐
                  │ Agent 节点 A     │   │ 新节点一键安装命令      │
                  │ monitor -mode    │   │ uname -m 自探测架构     │
                  │   agent          │   └────────────────────────┘
                  └──────────────────┘
```

- **server**：面板本体，提供 Web UI、接收心跳、执行告警、管理更新。
- **agent**：部署在被监控机器上，每 5 秒上报一次指标；每 20 秒做一轮 Ping 探测。
- **install**：Agent 的安装模式，自动识别 Systemd / OpenRC 并注册为开机自启服务。

面板把「待更新版本号」写进节点记录，Agent 下次心跳时就会收到更新指令并自行替换、
重启进程 —— 因此不需要在每台机器上手工操作。

---

## 快速开始

### 方式一：一键脚本（推荐）

```bash
curl -o install.sh https://raw.githubusercontent.com/jinhuaitao/Monitor/master/install.sh
chmod +x install.sh
sudo ./install.sh
```

脚本会检测 Systemd / OpenRC，下载对应架构的二进制、校验 SHA256、注册服务并启动。

也支持非交互调用，方便写进 cloud-init / 自动化脚本：

```bash
sudo ./install.sh install     # 安装或更新
sudo ./install.sh status      # 查看状态与版本
sudo ./install.sh restart
sudo ./install.sh uninstall
```

国内网络下载缓慢时，编辑脚本顶部的 `MIRROR`：

```sh
MIRROR="https://ghfast.top/"     # 留空为直连
```

### 方式二：手动部署

从 [Releases](https://github.com/jinhuaitao/Monitor/releases/latest) 下载对应架构的
`monitor-linux-amd64` 或 `monitor-linux-arm64`，然后：

```bash
chmod +x monitor-linux-amd64
./monitor-linux-amd64 -mode server -port 8080
```

前台运行确认无误后，再交给服务管理器托管。

---

## 首次配置

1. 浏览器打开 `http://<服务器IP>:8080`。
2. 首次访问会自动跳转到 `/setup`，创建管理员账号。
   口令要求：**至少 8 位**，且包含大写字母 / 小写字母 / 数字 / 符号中的**至少两类**。
3. 登录后点击右上角 **⚙️ 系统管理**，按需配置告警通道与外观。

**Agent 通信 Token 会自动生成**并写入数据库，无需手工设置。
它不会因为面板重启而变化（否则全部节点会一起掉线）。

> 面板默认是「公开看板」：任何人都能看到节点列表与负载，**但看不到 IP、
> 待更新版本等敏感字段**。如果需要完全私有，请参见[安全模型](#安全模型)。

---

## 接入被监控节点

1. 登录面板 → **⚙️ 系统管理** → **➕ 添加节点**。
2. 填写节点名称（例如 `香港-阿里云`），点击「生成并复制命令」。
3. 在被监控机器上粘贴并执行该命令。

命令形如：

```bash
A=$(uname -m); case "$A" in x86_64|amd64)A=amd64;; aarch64|arm64)A=arm64;; *)exit 1;; esac; \
rm -f monitor; curl -fL -o monitor "http://<面板地址>/api/download?arch=$A" && \
chmod +x monitor && ./monitor -mode install -server '<面板地址>' -token '<Token>' -id '<节点ID>'
```

架构由**目标机器自己**用 `uname -m` 探测 —— 面板此时并不知道对方是 amd64 还是 arm64，
下发错误架构的二进制只会得到 `cannot execute binary file`，比下载失败更难排查。

---

## 配置参考

### 命令行参数

| 参数 | 默认值 | 说明 |
| --- | --- | --- |
| `-mode` | `server` | `server` / `agent` / `install` / `version`。未知值会**报错退出**，不会被当成 server |
| `-port` | `8080` | 服务端监听端口（`server` 模式） |
| `-server` | `http://localhost:8080` | 面板地址（`agent` / `install` 模式） |
| `-token` | 空 | Agent 通信 Token（`agent` / `install` 模式必填） |
| `-id` | 空 | 节点 ID（`agent` / `install` 模式必填） |

```bash
./monitor -mode version      # 打印版本 / 提交 / 构建时间 / 平台
./monitor -h                 # 查看全部参数
```

### 环境变量

| 变量 | 说明 |
| --- | --- |
| `SESSION_KEY` | 会话 Cookie 的签名密钥，**至少 32 个字符**。不设置时面板每次启动随机生成，代价是重启后需要重新登录；设置了则跨重启保持登录态。设置过短会**拒绝启动** —— 弱密钥等于任何人都能伪造管理员会话。 |

### 面板内可调项

**系统管理 → 告警通知**

| 项 | 默认 | 说明 |
| --- | --- | --- |
| Telegram Bot Token / Chat ID | 空 | 两者都填才会推送 |
| Webhook 地址 | 空 | 钉钉 / 飞书 / Discord / Slack / 自建接收端 |
| Webhook 格式 | `generic` | `generic` / `dingtalk` / `feishu` / `discord` / `slack`。各平台报文结构不同，必须显式选择 |

**系统管理 → 告警规则**

| 项 | 默认 | 说明 |
| --- | --- | --- |
| 告警总开关 | 开 | 关闭后完全停止检测 |
| 离线告警 | 开 | 判定时长默认 30 秒（范围 15 ~ 86400） |
| 重复提醒间隔 | 0 分钟 | **0 表示只在状态变化时通知一次**，不会周期性轰炸 |
| CPU / 内存 / 磁盘阈值 | 90% | 填 0 表示关闭该项告警 |
| 告警历史保留 | 30 天 | 范围 1 ~ 3650 天 |

**系统管理 → 数据管理**

| 项 | 默认 | 说明 |
| --- | --- | --- |
| 监控历史保留 | 24 小时 | 范围 1 ~ 8760 小时 |
| 操作日志保留 | 90 天 | |
| 告警历史保留 | 30 天 | |

**系统管理 → 外观设置**：背景（默认渐变 / Bing 每日壁纸 / 自定义 URL）、
模糊度、卡片透明度、卡片内边距。

**系统管理 → 版本更新**：更新源仓库（`owner/repo`）、加速镜像（必须 `https://` 开头）、
更新后重启命令（留空则自动探测服务名）。

---

## 告警

- **状态翻转驱动**：每条规则（节点 × 类型）独立维护「是否处于告警中」。
  离线三天的节点只会发一条消息，不会每 5 秒刷一次。
- **恢复通知不受冷却限制**：故障结束了必须立刻让人知道，不会因为冷却窗口被吞掉。
- **维护模式 / 静音**在检测阶段直接短路，不会「先算完再丢掉」。
- **多通道并行推送**：Telegram 与 Webhook 各起一个协程，一个通道卡住不会拖住另一个。
- **告警事件落库**：可在「告警历史」中查看、按类型筛选、按时间清理或导出 CSV / JSON。

离线告警文案示例：

```
🔴 节点离线
名称：香港-阿里云
ID：a1b2c3
IP：1.2.3.4
已失联：5 分 30 秒
```

---

## 版本更新

### 面板自更新

**系统管理 → 版本更新 → 检查更新 → 立即更新**

流程：拉取 GitHub Release → 下载对应架构二进制 → **校验** → 写脚本替换自身并重启。

限制与保护：

- 仅支持 **Linux** 运行环境；
- **Docker 环境会主动拒绝**（应改用拉取新镜像）；
- 更新前先做目录可写性预检，避免下载完才发现无法替换；
- 替换前做三重体检：文件 ≥ 1 MiB、ELF 头正确、机器类型与目标架构一致。

> 为什么 SHA256 校验不够：面板会把缓存的这份文件下发给**所有**节点，
> 节点收到后直接替换自身并重启。若放进来的是错误页或被截断的下载，
> 后果不是「某个节点更新失败」，而是全部节点一起起不来，且没法从面板上救回来。
> 而 SHA256 来自同一个仓库 —— 坏源配坏哈希，永远对得上。

### 客户端更新

**系统管理 → 版本更新 → 同步最新版本**：面板预下载各架构 Agent 二进制到本地 `agents/`，
同样做三重体检，通过后才留在缓存里。

然后 **下发更新**（单个节点或全部）：面板把待更新版本写入节点记录，
Agent 下次心跳（≤ 5 秒）时收到指令，从 `/api/agent/binary` 下载并自行替换重启。

### 更新源安全

「更新源」在效果上等价于**一条对所有被控服务器的执行通道**（指令落到节点后
以 root 身份替换二进制并重启），因此：

- 仓库地址必须匹配 `owner/repo` 形态，畸形配置会**回退到官方仓库**而不是被当成更新源；
- 加速镜像必须 `https://` 开头 —— 用 http 会让整条更新链降级成明文；
- 任何修改更新源的操作都会写入审计日志。

---

## 数据与备份

所有数据都在运行目录下的 SQLite 文件里：

| 文件 | 说明 |
| --- | --- |
| `monitor.db` | 主库 |
| `monitor.db-wal` | 预写日志（已启用 WAL 模式） |
| `monitor.db-shm` | 共享内存索引 |
| `agents/monitor-linux-{amd64,arm64}` | 缓存的 Agent 二进制 |

> **不要直接 `cp monitor.db`。** 开启 WAL 后，新提交的事务先写入 `-wal`，
> 主文件在 checkpoint 之前可能根本没有最新数据 —— 直接拷主文件有机会
> 得到一个「能打开但数据是旧的」甚至空的库，而使用者要到恢复那天才会发现。

**正确做法**：系统管理 → 数据管理 → **下载备份**。

该功能使用 SQLite 原生的 `VACUUM INTO`，在读事务里取快照，产出一个已合并 WAL 的
自洽单文件；生成后还会**回读校验**（文件头 + `PRAGMA integrity_check` + 行数统计），
确认可用才交给浏览器下载。

**恢复**：停掉面板，用备份文件替换 `monitor.db`（同时删除 `monitor.db-wal` 与
`monitor.db-shm`），再启动即可。

---

## 安全模型

### 已经做到的

| 项 | 实现 |
| --- | --- |
| 口令存储 | bcrypt（cost 12）。老库的 `salt$sha256` 格式仍可登录，并在登录成功的那一刻原地升级 |
| 口令强度 | 至少 8 位、至少两类字符、拒绝常见弱口令、不可与用户名相同 |
| 登录限流 | **按来源 IP** 连续失败 5 次锁定 10 分钟；锁定期间不再累加计数（否则攻击者持续请求就能无限延长锁定） |
| 来源判定 | 显式 `SetTrustedProxies(nil)`，`RemoteIP()` 一律取真实 TCP 对端，`X-Forwarded-For` 无法伪造 |
| 会话失效 | 改口令 / 改用户名后会话版本前进一格，所有设备上的旧 Cookie 立即作废 |
| 会话 Cookie | `HttpOnly` + `SameSite=Strict`，`Secure` 在 HTTPS 下自动开启 |
| Agent 鉴权 | Token 以**常量时间**比较 |
| 输入收敛 | 上报字段去控制字符 + 限长；百分比夹到 0~100；容量超过 1 PiB 视为无效 |
| 请求体上限 | 2 MiB，避免超大 POST 打满内存 |
| HTTP 超时 | `ReadHeaderTimeout` 10s（防 Slowloris）/ `ReadTimeout` 2min / `WriteTimeout` 10min / `IdleTimeout` 2min |
| 响应头 | `X-Content-Type-Options` / `X-Frame-Options` / `Referrer-Policy` / `X-Robots-Tag` / `Permissions-Policy`，HTTPS 下追加 HSTS |
| 审计日志 | 改 Token、改更新源、下发更新、改口令、导出、备份、清理等全部留痕（含来源 IP） |
| 更新校验 | ELF 头 + 机器类型 + SHA256 三重校验，任一项不过即拒绝安装 |

### 需要你注意的

1. **仪表盘默认是公开的。** 未登录访客可以看到节点名称、负载、运行时长、在线状态
   （IP 与待更新版本会被隐藏）。这是「公开状态页」的设计取向。
   如果面板暴露在公网且不希望如此，请在反向代理层加一道访问控制
   （Basic Auth / IP 白名单 / 仅内网可达），或把面板部署在内网并通过 VPN 访问。

2. **务必使用 HTTPS。** 面板本身不终止 TLS。请在前面放一层反向代理
   （Caddy / Nginx + certbot），否则登录口令与 Agent Token 都会明文传输。
   反代请传递 `X-Forwarded-Proto: https`，这样会话 Cookie 会自动带上 `Secure`。

3. **`SESSION_KEY` 请用强随机值**：

   ```bash
   openssl rand -hex 32
   ```

4. **`monitor.db` 等同于全部凭据。** 里面含管理员口令哈希、Agent Token、
   全部节点信息与审计日志。请确保文件权限为 `600`，且不要提交到仓库
   （`.gitignore` 已覆盖）。

5. **`/api/download` 无需登录**，任何能访问面板的人都能下载 Agent 二进制。
   这是为了让安装命令可直接执行。二进制本身不含任何凭据。

6. **归属地查询**会把节点 IP 发送给第三方 `ip-api.com`（免费接口仅支持 http）。
   若你介意，可以在 `main.go` 中移除 `resolveCountryAsync` 的调用。

---

## HTTP 接口

### 公开

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/` | 仪表盘（未登录时隐藏敏感字段） |
| GET/POST | `/setup` | 初始化管理员（**仅面板未初始化时可用**） |
| GET/POST | `/login` | 登录 |
| GET | `/logout` | 退出 |
| GET | `/api/stats` | 全部节点实时状态 |
| GET | `/api/history/ping` | 单节点 Ping 历史 |
| GET | `/api/history/full` | 单节点完整历史（ping / cpu / mem / disk） |
| GET | `/api/bing` | Bing 每日壁纸代理 |
| GET | `/api/download?arch=` | 下载 Agent 二进制（不带 `arch` 时分发面板自身） |
| GET | `/manifest.webmanifest` · `/sw.js` · `/offline.html` · `/icons/*` · `/favicon.ico` | PWA 资源 |
| GET | `/healthz` | 健康探针，只回 `ok`（供离线页与容器健康检查使用） |
| GET | `/robots.txt` | 要求爬虫不要收录 |

### 需要 Token（Agent 专用）

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| POST | `/api/report` | 上报心跳。Token 从 `Authorization` 头或 `?token=` 读取 |
| GET | `/api/agent/binary?arch=` | Agent 自更新下载 |

### 需要登录（`/api/settings/*`）

节点管理（创建 / 改名 / 分组 / 隐藏 / 删除 / 批量）、监控目标、告警通道与规则、
告警历史、外观、账号与口令、操作日志、系统信息、数据统计 / 保留策略 / 清理 / 导出 / 备份、
更新源与更新触发。

---

## 开发

### 环境

- Go 1.23 及以上（`go.mod` 声明的语言版本是 `1.23`；CI 使用 Go 1.27 构建）
- 无需 gcc：依赖链全部是纯 Go（SQLite 使用 `glebarez/sqlite`）

### 本地构建

```bash
# 静态编译（与 CI 一致，务必保持 CGO_ENABLED=0）
CGO_ENABLED=0 go build -trimpath -o monitor .

# 静态检查
gofmt -l .
go vet ./...

# 交叉编译
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o monitor-linux-arm64 .
```

### 版本注入

```bash
CGO_ENABLED=0 go build -trimpath \
  -ldflags "-s -w \
    -X main.BuildVersion=2026.09.30-abc1234 \
    -X main.BuildCommit=abc1234 \
    -X main.BuildTime=2026-09-30T08:00:00Z" \
  -o monitor .
```

### 源码结构

| 文件 | 职责 |
| --- | --- |
| `main.go` | 入口、路由、Agent 逻辑、SQLite 与 HTTP 服务配置、输入收敛 |
| `admin.go` | 系统管理：审计、登录限流、账号安全、系统信息、数据管理、节点批量操作 |
| `alert.go` | 告警规则引擎、状态机、通知通道、告警历史 |
| `update.go` | 版本检查、面板自更新、Agent 同步与下发、二进制校验 |
| `pwa.go` | manifest、Service Worker、离线页、运行时绘制的图标 |
| `ui.go` | 内嵌的仪表盘与登录页模版（HTML + CSS + JS） |
| `version.go` | 构建期注入的版本变量 |
| `install.sh` | 服务端安装 / 升级 / 卸载脚本 |
| `Dockerfile` / `.dockerignore` | 容器镜像 |
| `.github/workflows/build.yml` | CI：格式检查 → vet → 编译门禁 → 交叉编译 → 静态链接校验 → 发布 |

### CI 做了什么

1. `gofmt` 检查、`go vet`、编译门禁（`-mod=readonly`）；
2. 交叉编译 `linux/amd64` 与 `linux/arm64`；
3. **静态链接防回归校验**：检查 ELF 是否含 `INTERP` 段、是否引用 `GLIBC_` 符号
   —— 一旦有人重新打开 CGO，Alpine 用户会立刻遇到启动崩溃；
4. 冒烟测试：二进制能自报版本、未知 `-mode` 会报错退出；
5. 生成 `.sha256` 校验文件，统一发布到 `latest` 标签。

---

## 常见问题

**Q：面板重启后需要重新登录？**
设置了 `SESSION_KEY` 就能跨重启保持登录态。不设置时面板每次启动随机生成会话密钥，
这是有意的（「重启即让全部会话失效」），但代价就是需要重新登录。

**Q：节点显示「等待接入…」一直不变？**
按顺序排查：① 节点上 `systemctl status monitor`（或 `rc-service monitor status`）
看 Agent 是否在跑；② 面板地址在节点上能否 `curl` 通；③ Token 是否与面板一致
（在「系统管理」里可以看到当前 Token）；④ 节点时间是否严重偏离。

**Q：改了 Token，全部节点都掉线了？**
预期行为。Token 是 Agent 身份的唯一凭据，改动后所有已安装的 Agent 都需要重新鉴权。
请重新生成安装命令并在节点上重跑。

**Q：Alpine 上启动即崩溃？**
早期版本用 `CGO_ENABLED=1` 在 Ubuntu 上构建，产出的是 glibc 动态链接二进制，
在 musl 环境下会因为缺少 `/lib64/ld-linux-x86-64.so.2` 直接退出。
现在 CI 强制 `CGO_ENABLED=0` 并加了防回归校验，请使用最新 Release。

**Q：Docker 里点「立即更新」提示不支持？**
预期行为。镜像内的二进制会被下一次 `docker run` 覆盖，所以面板主动拒绝了自更新。
请用 `docker compose pull && docker compose up -d` 升级。

**Q：告警一直不触发？**
检查：① 告警总开关；② 该节点是否处于**维护模式**或**告警静音**；③ 阈值是否填了 0
（0 表示关闭该项）；④ 通知通道是否配置完整（Telegram 需要 Token 与 Chat ID 都填）。
可以用「发送测试」按钮先验证通道本身是否可用。

**Q：备份文件打开是空的 / 数据不全？**
说明用的是 `cp monitor.db`。WAL 模式下必须用面板的「下载备份」（内部走
`VACUUM INTO`），或先执行 `PRAGMA wal_checkpoint(TRUNCATE)` 再拷贝。

**Q：面板可以放在 Nginx 后面吗？**
可以。请传递 `X-Forwarded-Proto`（让 Cookie 自动带上 `Secure`）。
但**不要**依赖 `X-Forwarded-For` 做来源判定 —— 面板刻意不信任任何代理头，
登录限流一律按真实 TCP 对端计数。

---

## License

本项目未附带 License 文件。若需开源分发，请先补充一个明确的许可证
（例如 MIT / Apache-2.0），否则默认保留全部权利。
