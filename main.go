package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite" // 纯 Go SQLite 驱动：无需 CGO，可静态编译
	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/disk"
	"github.com/shirou/gopsutil/v3/host"
	"github.com/shirou/gopsutil/v3/mem"
	gonet "github.com/shirou/gopsutil/v3/net"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ================= 全局配置 =================

type PingTargetConfig struct {
	Target string `json:"target"`
	Alias  string `json:"alias"`
}

// dbPath 面板数据库文件位置。放在变量里而不是到处硬编码 "monitor.db"：
// 系统信息页要显示真实路径，数据备份也要按它去找文件。
//
// 这里只保存【纯路径】，不带任何查询参数 —— databasePath() / databaseSize()
// 会拿它去 stat 文件、admin.go 还要拼进 VACUUM INTO，带上 "?_pragma=…"
// 会把这些地方全部拼错。连接串由 sqliteDSN() 单独生成。
var dbPath = "monitor.db"

// SQLite 运行参数，以 DSN 的 _pragma 查询项下发，由驱动在【每条连接】建立时
// 逐条执行（modernc 系驱动的 _pragma 语义，已在本地实测确认生效）。
//
// 为什么必须显式写：不写时 journal_mode 就是默认的 delete（已实测），
// 全部读写共用同一把文件锁 —— 十几个节点 5 秒一次心跳，外加告警落库、
// 历史清理、审计写入，很容易撞成 "database is locked"。
// 开启 WAL 后读不再阻塞写，是这种「单文件 + 多协程」面板能长期稳跑的前提。
//
//	journal_mode(WAL)    读并发不阻塞写，崩溃恢复也更可靠
//	busy_timeout(5000)   遇到锁等待 5 秒而不是立即报错（驱动默认已是 5000，写明便于排障）
//	synchronous(NORMAL)  WAL 下的推荐搭配：依然保证不损坏，省掉每次提交的 fsync
const (
	dbPragmaJournal = "journal_mode(WAL)"
	dbPragmaBusy    = "busy_timeout(5000)"
	dbPragmaSync    = "synchronous(NORMAL)"
)

// sqliteDSN 把纯路径转成带 PRAGMA 的连接串
func sqliteDSN(path string) string {
	return path + "?_pragma=" + dbPragmaJournal +
		"&_pragma=" + dbPragmaBusy +
		"&_pragma=" + dbPragmaSync
}

// tuneSQLitePool 约束连接池。
//
// WAL 允许「一个写 + 多个读」并行，但写与写之间仍要等 busy_timeout。
// 池子开太大只会把等待排成更长的队列，开太小又会拖慢读接口，
// 这里取一个保守上限，并定期回收空闲连接以免长期占着文件句柄。
func tuneSQLitePool(g *gorm.DB) {
	sqlDB, err := g.DB()
	if err != nil {
		return
	}
	sqlDB.SetMaxOpenConns(8)
	sqlDB.SetMaxIdleConns(4)
	sqlDB.SetConnMaxLifetime(time.Hour)
}

var (
	statusCache = make(map[string]SystemStatus)
	cacheMutex  sync.RWMutex
	db          *gorm.DB

	globalConfig struct {
		sync.RWMutex
		Token      string
		ServerURL  string
		TGToken    string
		TGChatID   string
		WebhookURL string
		// WebhookFormat 决定推给 Webhook 的 JSON 结构（钉钉 / 飞书 / Discord / Slack 各不相同）
		WebhookFormat string
		// === 外观配置 ===
		SiteTheme   string
		BgType      string
		BgCustomURL string
		BgBlur      int
		CardOpacity float64
		CardPadding int
		// ===============
		PingTargets []PingTargetConfig
		// === 更新配置 ===
		UpdateRepo         string // GitHub 仓库 owner/repo
		UpdateProxy        string // 下载加速镜像前缀
		RestartCmd         string // 更新后重启命令（留空自动检测）
		AgentBundleVersion string // 面板已缓存的客户端版本
		// === 告警规则（详见 alert.go）===
		AlertEnabled     bool
		AlertOffline     bool
		AlertOfflineSec  int
		AlertCooldownMin int
		AlertCPU         float64
		AlertMem         float64
		AlertDisk        float64
		AlertKeepDays    int
		// === 数据保留 ===
		HistoryKeepHours int
		AuditKeepDays    int
		// === 会话版本：改密码后自增，旧 Cookie 立即失效 ===
		SessionEpoch string
	}
)

// errSetupClosed 表示面板已完成初始化，/setup 的建号入口必须关闭
var errSetupClosed = errors.New("setup already completed")

// ================= 数据库模型 =================

type User struct {
	ID       uint   `gorm:"primaryKey"`
	Username string `gorm:"unique"`
	Password string // 格式: salt$hash
}

type AppConfig struct {
	Key   string `gorm:"primaryKey"`
	Value string
}

type Node struct {
	AgentID       string `gorm:"primaryKey"`
	Name          string
	HideID        bool `gorm:"default:true"`
	SortOrder     int  `gorm:"default:0"`
	Denied        bool `gorm:"default:false"`
	CountryCode   string
	Arch          string    // 客户端 CPU 架构 (amd64/arm64)
	AgentVersion  string    // 客户端上报的程序版本
	PendingUpdate string    // 待下发的客户端版本号
	CreatedAt     time.Time // [新增] 用于记录添加时间
	Group         string    `gorm:"default:''"`    // [新增] 分组，用于节点多时的归类与筛选
	Maintenance   bool      `gorm:"default:false"` // [新增] 维护模式：期间不触发任何告警
	AlertMuted    bool      `gorm:"default:false"` // [新增] 仅静音告警，但节点仍正常显示
}

type MonitorHistory struct {
	ID        uint   `gorm:"primaryKey"`
	AgentID   string `gorm:"index"`
	Type      string `gorm:"index"`
	Target    string
	Value     float64
	CreatedAt time.Time `gorm:"index"`
}

// ================= 传输模型 =================

type SystemStatus struct {
	AgentID         string             `json:"agent_id"`
	Name            string             `json:"name"`
	HideID          bool               `json:"hide_id"`
	SortOrder       int                `json:"sort_order"`
	CountryCode     string             `json:"country_code"`
	PingTargets     []PingTargetConfig `json:"ping_targets"`
	OS              string             `json:"os"`
	IP              string             `json:"ip"`
	Uptime          uint64             `json:"uptime"`
	CPUUsage        float64            `json:"cpu_usage"`
	MemUsedPercent  float64            `json:"mem_used_percent"`
	DiskUsedPercent float64            `json:"disk_used_percent"`
	CPUModel        string             `json:"cpu_model"`  // [新增] CPU 型号
	MemTotal        uint64             `json:"mem_total"`  // [新增] 物理内存总量（字节）
	DiskTotal       uint64             `json:"disk_total"` // [新增] 根分区总容量（字节）
	NetInSpeed      uint64             `json:"net_in_speed"`
	NetOutSpeed     uint64             `json:"net_out_speed"`
	NetTotalIn      uint64             `json:"net_total_in"`
	NetTotalOut     uint64             `json:"net_total_out"`
	PingResults     map[string]int64   `json:"ping_results"`
	LastUpdate      time.Time          `json:"last_update"`
	InstallTime     int64              `json:"install_time"`   // [新增] 用于前端排序
	Version         string             `json:"version"`        // [新增] 客户端/面板程序版本
	Arch            string             `json:"arch"`           // [新增] CPU 架构
	PendingUpdate   string             `json:"pending_update"` // [新增] 待更新版本（仅管理员可见）
	Group           string             `json:"group"`          // [新增] 节点分组
	Maintenance     bool               `json:"maintenance"`    // [新增] 维护模式
	AlertMuted      bool               `json:"alert_muted"`    // [新增] 告警静音
}

// UpdateCommand 下发给 Agent 的自更新指令
type UpdateCommand struct {
	Version string `json:"version"`
	URL     string `json:"url"`
	SHA256  string `json:"sha256"`
}

type AgentResponse struct {
	Status      string             `json:"status"`
	PingTargets []PingTargetConfig `json:"ping_targets"`
	Update      *UpdateCommand     `json:"update,omitempty"`
}

// ================= HTML 模版 =================

// 模版只在服务端启动时解析一次，之后所有请求复用同一个 *template.Template。
//
// 早期实现是每个请求 template.New(...).Parse(htmlDashboard) 一遍，
// 并且把返回的 error 丢掉。这有两个问题：
//   - 性能：仪表盘模版近 150KB，每次打开页面都要重新做一遍词法/语法分析与
//     JS 上下文推导，纯属白烧 CPU；
//   - 可诊断性：Parse 失败会返回 nil，接着 Execute 就是空指针 panic ——
//     现场只剩一个 500，看不出是模版坏了。
//
// 模版是编译进二进制的常量，解析失败等同构建事故，所以放在启动路径上
// 直接失败（而不是在 agent / install 模式下也白白解析一遍）。
var (
	dashboardTmpl *template.Template
	loginTmpl     *template.Template
)

func parseTemplates() error {
	var err error
	if dashboardTmpl, err = template.New("dashboard").Parse(htmlDashboard); err != nil {
		return fmt.Errorf("解析仪表盘模版失败: %w", err)
	}
	if loginTmpl, err = template.New("login").Parse(htmlLogin); err != nil {
		return fmt.Errorf("解析登录模版失败: %w", err)
	}
	return nil
}

// renderHTML 统一渲染入口。
//
// 显式写 Content-Type 而不是依赖 net/http 的内容嗅探：嗅探只在第一次 Write 时
// 发生，一旦前面误写了别的字节就会退化成 text/plain。同时给页面加 no-store ——
// 登录页与仪表盘里内联着 Agent 通信 Token、更新源等敏感配置，
// 让它们进浏览器/中间层缓存既可能读到陈旧配置，也可能在共用设备上泄露。
func renderHTML(c *gin.Context, t *template.Template, data map[string]interface{}) {
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.Header("Cache-Control", "no-store")
	if err := t.Execute(c.Writer, data); err != nil {
		log.Printf("[ui] 渲染模版失败: %v", err)
	}
}

// ================= 主程序 =================

func main() {
	mode := flag.String("mode", "server", "运行模式: server / agent / install / version")
	port := flag.String("port", "8080", "服务端监听端口（server 模式）")
	sAddr := flag.String("server", "http://localhost:8080", "面板地址（agent / install 模式）")
	tkn := flag.String("token", "", "Agent 通信 Token（agent / install 模式）")
	aid := flag.String("id", "", "Agent 节点 ID（agent / install 模式）")

	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(),
			"Hub Monitor %s —— 轻量级服务器监控面板\n\n用法: %s [选项]\n\n选项:\n",
			displayVersion(), filepath.Base(os.Args[0]))
		flag.PrintDefaults()
	}
	flag.Parse()

	switch *mode {
	case "agent":
		if *tkn == "" || *aid == "" {
			fmt.Fprintln(os.Stderr, "错误: agent 模式必须提供 -token 与 -id")
			os.Exit(2)
		}
		runAgent(*sAddr, *tkn, *aid)
	case "install":
		if *tkn == "" || *aid == "" {
			fmt.Fprintln(os.Stderr, "错误: install 模式必须提供 -token 与 -id")
			os.Exit(2)
		}
		installAgent(*sAddr, *tkn, *aid)
	case "version", "v", "-v", "--version":
		fmt.Printf("Hub Monitor %s\ncommit: %s\nbuilt:  %s\nplatform: %s/%s\n",
			displayVersion(), BuildCommit, BuildTime, runtime.GOOS, runtime.GOARCH)
	case "server":
		runServer(*port)
	default:
		// 早期版本把「不认识的模式」也当成 server 启动。
		// 那会让 `-mode agnet` 这种拼写错误变成一个看起来正常、实际行为
		// 完全不同的进程（占住 8080 端口），排查起来非常费劲。
		fmt.Fprintf(os.Stderr, "错误: 未知模式 %q（可用: server / agent / install / version）\n", *mode)
		flag.Usage()
		os.Exit(2)
	}
}

// ================= 安装逻辑 =================

func installAgent(server, token, id string) {
	fmt.Println(">> 正在安装监控 Agent...")
	binPath, err := filepath.Abs(os.Args[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, "错误: 无法获取当前程序路径:", err)
		os.Exit(1)
	}
	if _, err := os.Stat("/etc/alpine-release"); err == nil {
		err = installOpenRC(binPath, server, token, id)
	} else {
		err = installSystemd(binPath, server, token, id)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "❌ 安装失败:", err)
		os.Exit(1)
	}
	fmt.Println("✅ 安装成功! 服务已启动并设置开机自启。")
}

// unitArg 把参数包成 systemd / OpenRC 都能正确还原的字面量。
//
// ExecStart 与 OpenRC 的 command_args 都按空白切分参数：面板地址或 Token 里
// 只要出现空格，服务就会带着被截断的参数启动 —— 表现为「脚本说装好了，
// 但节点永远不上线」，且现场日志里看不出任何异常。统一加双引号并转义
// 反斜杠与双引号，两种服务管理器都能正确还原。
func unitArg(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + r.Replace(s) + `"`
}

func installSystemd(binPath, server, token, id string) error {
	fmt.Println("-> 检测到 Systemd 系统")
	serviceContent := fmt.Sprintf(`[Unit]
Description=VPS Monitor Agent
After=network.target
[Service]
Type=simple
ExecStart=%s -mode agent -server %s -token %s -id %s
Restart=always
RestartSec=5
[Install]
WantedBy=multi-user.target
`, unitArg(binPath), unitArg(server), unitArg(token), unitArg(id))

	const unitPath = "/etc/systemd/system/monitor.service"
	// 早期实现忽略 WriteFile 的返回值，于是「没用 root 运行 / /etc 只读」时
	// 依然会打印「安装成功」，而服务其实根本不存在。这类假成功最难排查：
	// 用户以为自己装好了，回头只会怀疑网络。
	if err := os.WriteFile(unitPath, []byte(serviceContent), 0644); err != nil {
		return fmt.Errorf("写入 %s 失败（请确认以 root 运行）: %w", unitPath, err)
	}
	// daemon-reload 必须先成功，否则后面的 enable / restart 读不到新的 unit 文件
	if out, err := exec.Command("systemctl", "daemon-reload").CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl daemon-reload 失败: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	if out, err := exec.Command("systemctl", "enable", "monitor").CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl enable monitor 失败: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	// 用 restart 而不是 start：重复安装（升级）时也能真正加载新二进制
	if out, err := exec.Command("systemctl", "restart", "monitor").CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl restart monitor 失败: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func installOpenRC(binPath, server, token, id string) error {
	fmt.Println("-> 检测到 Alpine (OpenRC) 系统")
	scriptContent := fmt.Sprintf(`#!/sbin/openrc-run
name="monitor"
command=%s
command_args="-mode agent -server %s -token %s -id %s"
command_background=true
pidfile="/run/monitor.pid"
`, unitArg(binPath), unitArg(server), unitArg(token), unitArg(id))

	const initPath = "/etc/init.d/monitor"
	if err := os.WriteFile(initPath, []byte(scriptContent), 0755); err != nil {
		return fmt.Errorf("写入 %s 失败（请确认以 root 运行）: %w", initPath, err)
	}
	if out, err := exec.Command("rc-update", "add", "monitor", "default").CombinedOutput(); err != nil {
		return fmt.Errorf("rc-update add monitor default 失败: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	// 残留的 pidfile 会让 OpenRC 认定服务已在运行，于是「启动成功」但实际没起
	os.Remove("/run/monitor.pid")
	if out, err := exec.Command("rc-service", "monitor", "restart").CombinedOutput(); err != nil {
		return fmt.Errorf("rc-service monitor restart 失败: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// ================= 安装命令生成 =================

// installCmdTmpl 是面板下发给用户的 Agent 安装命令模板。
// 占位符由 installCommand() 替换；前端也会拿到同一份模板（面板模板变量 InstallTmpl），
// 这样「新建节点」与「复制已有节点命令」两处永远生成同一条命令，不会各自漂移。
//
// 为什么要让命令自己探测架构：面板此刻只知道有个节点要装，
// 并不知道那台机器是 amd64 还是 arm64 —— 只有对方自己 uname -m 才准。
// 下发错误架构的二进制，对方得到的只是 "cannot execute binary file"。
const installCmdTmpl = `A=$(uname -m);case "$A" in x86_64|amd64)A=amd64;;aarch64|arm64)A=arm64;;*)echo "不支持的架构: $A (仅支持 x86_64 / aarch64)";exit 1;;esac;rm -f monitor;curl -fL -o monitor "__SERVER__/api/download?arch=$A" && chmod +x monitor && ./monitor -mode install -server '__SERVER__' -token '__TOKEN__' -id '__ID__'`

// installCommand 把模板渲染成可直接粘贴执行的一条命令
func installCommand(serverURL, token, id string) string {
	return strings.NewReplacer(
		"__SERVER__", strings.TrimRight(serverURL, "/"),
		"__TOKEN__", token,
		"__ID__", id,
	).Replace(installCmdTmpl)
}

// ================= 服务端 =================

func runServer(port string) {
	dbPath = "monitor.db"
	var err error
	db, err = gorm.Open(sqlite.Open(sqliteDSN(dbPath)), &gorm.Config{})
	if err != nil {
		log.Fatalf("[fatal] 打开数据库失败: %v", err)
	}
	tuneSQLitePool(db)

	// 建表失败必须当场退出：硬撑下去只会得到一个「登录页打得开、一保存就 500」
	// 的半死状态，比启动失败难定位得多。
	if err := db.AutoMigrate(&User{}, &AppConfig{}, &Node{}, &MonitorHistory{}, &AuditLog{}, &AlertEvent{}); err != nil {
		log.Fatalf("[fatal] 数据库结构初始化失败: %v", err)
	}
	// 模版是编译进二进制的常量，解析失败等同构建事故，启动即失败
	if err := parseTemplates(); err != nil {
		log.Fatalf("[fatal] %v", err)
	}

	loadGlobalConfig()
	go monitorAlerts()
	go cleanupMaintenance()

	gin.SetMode(gin.ReleaseMode)
	r := gin.Default()

	// 安全增强：不信任任何代理头。
	// gin 默认把 X-Forwarded-For 当作可信来源，于是 ClientIP() 可被请求方随意伪造 ——
	// 对「登录限流」这种按 IP 计数的防护来说，等于形同虚设。
	// 这里显式关掉信任代理，ClientIP()/RemoteIP() 一律回落到真实 TCP 对端。
	_ = r.SetTrustedProxies(nil)

	// 统一安全响应头 + 请求体上限
	r.Use(securityHeaders(), limitBodySize(2<<20))

	// PWA：manifest / Service Worker / 运行时绘制的图标 / 离线页
	registerPWARoutes(r)

	// 明确要求爬虫不要收录。面板可能直接暴露在公网，默认的仪表盘对
	// 未登录访客可见（只隐藏 IP），被搜索引擎收录后节点名称与负载情况
	// 就等于公开了。X-Robots-Tag 已覆盖现代爬虫，这里再给一份传统声明。
	r.GET("/robots.txt", func(c *gin.Context) {
		c.Header("Cache-Control", "public, max-age=86400")
		c.String(http.StatusOK, "User-agent: *\nDisallow: /\n")
	})

	// 安全增强: 随机生成 Session Key
	sessionKey, err := sessionKeyFromEnv()
	if err != nil {
		log.Fatalf("[fatal] %v", err)
	}
	store := cookie.NewStore(sessionKey)
	// 安全增强: Cookie 属性设置
	// Secure 交给每个请求单独判定（见 saveSession / sessionOptions）：
	// 面板常以明文 HTTP 部署在内网，无条件打开会让浏览器直接丢掉会话 Cookie，
	// 表现为「登录成功但立刻又跳回登录页」。
	store.Options(sessions.Options{Path: "/", MaxAge: 3600 * 24, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	r.Use(sessions.Sessions("mysession", store))

	// 下载程序本体：按 ?arch= 分发对应架构的 Agent 二进制。
	// 不带 arch 参数时保持旧行为（分发面板自身二进制）。
	r.GET("/api/download", func(c *gin.Context) {
		raw := c.Query("arch")
		if raw == "" {
			c.File("./monitor")
			return
		}
		arch := normalizeArch(raw)
		if arch != "amd64" && arch != "arm64" {
			c.String(400, "不支持的架构: %s（仅支持 amd64 / arm64）\n", raw)
			return
		}
		// 优先分发面板已缓存的对应架构二进制
		if p := agentBinaryPath(arch); fileExists(p) {
			c.Header("X-Binary-Arch", arch)
			c.FileAttachment(p, "monitor")
			return
		}
		// 没缓存时，只有面板自身就是该架构，才可以拿自己顶上。
		// 否则宁可明确报错，也不能下发错误架构的二进制 —— 对方拿到的只会是
		// "cannot execute binary file"，比下载失败更难排查。
		if arch == runtime.GOARCH {
			c.Header("X-Binary-Arch", arch)
			c.File("./monitor")
			return
		}
		c.String(404, "面板尚未缓存 %s 架构的 Agent 二进制（面板自身为 %s）。\n"+
			"请先在面板「系统管理 → 版本更新」点击「同步最新版本」，再重新执行安装命令。\n",
			arch, runtime.GOARCH)
	})

	// Agent 自更新专用下载（需 Token）
	r.GET("/api/agent/binary", func(c *gin.Context) {
		globalConfig.RLock()
		t := globalConfig.Token
		globalConfig.RUnlock()
		clientToken := c.GetHeader("Authorization")
		if clientToken == "" {
			clientToken = c.Query("token")
		}
		if !tokenEqual(clientToken, t) {
			c.AbortWithStatus(401)
			return
		}

		arch := normalizeArch(c.Query("arch"))
		if arch != "amd64" && arch != "arm64" {
			c.AbortWithStatus(400)
			return
		}
		p := agentBinaryPath(arch)
		if !fileExists(p) {
			c.AbortWithStatus(404)
			return
		}
		c.FileAttachment(p, "monitor")
	})

	r.GET("/", func(c *gin.Context) {
		var cnt int64
		db.Model(&User{}).Count(&cnt)
		if cnt == 0 {
			c.Redirect(302, "/setup")
			return
		}
		dashboardHandler(c)
	})

	r.GET("/setup", func(c *gin.Context) {
		var cnt int64
		db.Model(&User{}).Count(&cnt)
		if cnt > 0 {
			c.Redirect(302, "/login")
			return
		}
		globalConfig.RLock()
		theme := globalConfig.SiteTheme
		bgType := globalConfig.BgType
		bgUrl := globalConfig.BgCustomURL
		bgBlur := globalConfig.BgBlur
		cardOp := globalConfig.CardOpacity
		globalConfig.RUnlock()
		renderHTML(c, loginTmpl, map[string]interface{}{
			"Action":   "/setup",
			"Title":    "初始化设置",
			"Subtitle": "创建管理员账号",
			"BtnText":  "立即注册",
			"Theme":    theme,
			"BgType":   bgType, "BgCustomURL": bgUrl, "BgBlur": bgBlur, "CardOpacity": cardOp,
			"Version": displayVersion(),
			"Err":     c.Query("err"),
		})
	})
	r.POST("/setup", func(c *gin.Context) {
		// 安全修复：这里必须和 GET /setup 一样校验「面板是否已初始化」。
		// 早期版本只在 GET 里判断，POST 直接建号 —— 等于面板装好之后
		// 仍留着一个匿名可用的「创建管理员」后门，任何能访问面板的人
		// 都能拿到完整管理权限（含向全部节点下发更新）。
		u, p := c.PostForm("username"), c.PostForm("password")
		if u == "" || p == "" {
			c.Redirect(302, "/setup")
			return
		}
		// 初始管理员同样受密码强度约束：这是整条权限链的根，最不该被设成 123456
		if msg := passwordWeakness(u, p); msg != "" {
			c.Redirect(302, "/setup?err="+url.QueryEscape(msg))
			return
		}
		// 放进事务里做「计数 + 建号」：两个并发请求同时读到 cnt==0 时，
		// 单靠 Count 判断会双双建号成功，事务能把这层竞态关掉。
		err := db.Transaction(func(tx *gorm.DB) error {
			var cnt int64
			if err := tx.Model(&User{}).Count(&cnt).Error; err != nil {
				return err
			}
			if cnt > 0 {
				return errSetupClosed
			}
			return tx.Create(&User{Username: u, Password: hashPwd(p)}).Error
		})
		if err != nil {
			// 已初始化（或并发抢跑失败）一律回到登录页，不再泄露任何信息
			c.Redirect(302, "/login")
			return
		}
		auditAs(c, u, "setup", u, "创建管理员账号", true)
		c.Redirect(302, "/login")
	})
	r.GET("/login", func(c *gin.Context) {
		var cnt int64
		db.Model(&User{}).Count(&cnt)
		if cnt == 0 {
			c.Redirect(302, "/setup")
			return
		}
		if isAdminSession(c) {
			c.Redirect(302, "/")
			return
		}
		globalConfig.RLock()
		theme := globalConfig.SiteTheme
		bgType := globalConfig.BgType
		bgUrl := globalConfig.BgCustomURL
		bgBlur := globalConfig.BgBlur
		cardOp := globalConfig.CardOpacity
		globalConfig.RUnlock()
		renderHTML(c, loginTmpl, map[string]interface{}{
			"Action":   "/login",
			"Title":    "登录",
			"Subtitle": "请登录以管理您的节点",
			"BtnText":  "登 录",
			"Theme":    theme,
			"BgType":   bgType, "BgCustomURL": bgUrl, "BgBlur": bgBlur, "CardOpacity": cardOp,
			"Version": displayVersion(),
			"Err":     c.Query("err"),
		})
	})
	r.POST("/login", func(c *gin.Context) {
		u, p := c.PostForm("username"), c.PostForm("password")

		// ① 先看这个来源是否已被限流。判定必须放在校验密码之前，
		//    否则爆破方每猜一次都还能拿到「密码对不对」的信息量。
		if wait, blocked := loginBlocked(c); blocked {
			auditAs(c, u, "login_blocked", u,
				fmt.Sprintf("来源连续失败次数超限，剩余锁定 %d 秒", wait), false)
			c.Redirect(302, "/login?err="+url.QueryEscape(
				fmt.Sprintf("失败次数过多，请在 %d 秒后重试", wait)))
			return
		}

		var user User
		// 安全增强: 校验加盐哈希（老库为 SHA-256，登录成功后自动升级为 bcrypt）
		if db.Where("username=?", u).First(&user).Error == nil {
			if ok, needUpgrade := checkPwdUpgrade(p, user.Password); ok {
				if needUpgrade {
					db.Model(&User{}).Where("id = ?", user.ID).
						Update("password", hashPwd(p))
				}
				loginSucceeded(c)
				s := sessions.Default(c)
				s.Set("user", u)
				s.Set("epoch", sessionEpoch())
				saveSession(c, s)
				auditAs(c, u, "login", u, "登录成功", true)
				c.Redirect(302, "/")
				return
			}
		}

		// 失败：累计计数并告知剩余次数（对真正的管理员有用，对爆破方只是延迟）
		left := loginFailed(c, u)
		auditAs(c, u, "login", u, "用户名或密码错误", false)
		msg := "用户名或密码错误"
		if left > 0 {
			msg = fmt.Sprintf("用户名或密码错误，还可尝试 %d 次", left)
		} else {
			msg = "失败次数过多，账号已临时锁定，请稍后重试"
		}
		c.Redirect(302, "/login?err="+url.QueryEscape(msg))
	})
	r.GET("/logout", func(c *gin.Context) {
		audit(c, "logout", "", "退出登录", true)
		s := sessions.Default(c)
		s.Clear()
		saveSession(c, s)
		c.Redirect(302, "/")
	})

	api := r.Group("/api")
	{
		api.POST("/report", func(c *gin.Context) {
			globalConfig.RLock()
			t := globalConfig.Token
			targets := globalConfig.PingTargets
			globalConfig.RUnlock()
			// 安全增强: 优先从 Header 获取 Token
			clientToken := c.GetHeader("Authorization")
			if clientToken == "" {
				clientToken = c.Query("token")
			} // 兼容旧方式

			if !tokenEqual(clientToken, t) {
				c.AbortWithStatus(401)
				return
			}

			var s SystemStatus
			if err := c.ShouldBindJSON(&s); err == nil {
				// 先收敛不可信字段，再进入缓存 / 数据库 / 界面
				sanitizeReport(&s)
				s.LastUpdate = time.Now()
				if s.IP == "" {
					s.IP = c.ClientIP()
				}

				var node Node
				db.Clauses(clause.OnConflict{DoNothing: true}).Create(&Node{AgentID: s.AgentID})
				db.First(&node, "agent_id = ?", s.AgentID)

				if node.Denied {
					c.JSON(200, AgentResponse{Status: "stop"})
					return
				}

				if node.CountryCode == "" {
					go resolveCountryAsync(s.AgentID, s.IP)
				}

				// [新增] 同步客户端架构 / 版本，并处理更新回执
				if s.Arch != "" {
					node.Arch = normalizeArch(s.Arch)
				}
				if s.Version != "" {
					node.AgentVersion = s.Version
				}
				nodeUpdates := map[string]interface{}{}
				if s.Arch != "" {
					nodeUpdates["arch"] = normalizeArch(s.Arch)
				}
				if s.Version != "" {
					nodeUpdates["agent_version"] = s.Version
				}
				if node.PendingUpdate != "" && s.Version == node.PendingUpdate {
					nodeUpdates["pending_update"] = ""
					node.PendingUpdate = ""
				}
				if len(nodeUpdates) > 0 {
					db.Model(&Node{}).Where("agent_id = ?", s.AgentID).Updates(nodeUpdates)
				}

				for target, delay := range s.PingResults {
					if delay > 0 {
						db.Create(&MonitorHistory{AgentID: s.AgentID, Type: "ping", Target: target, Value: float64(delay), CreatedAt: time.Now()})
					}
				}
				if time.Now().Second() < 5 {
					db.Create(&MonitorHistory{AgentID: s.AgentID, Type: "cpu", Value: s.CPUUsage, CreatedAt: time.Now()})
					db.Create(&MonitorHistory{AgentID: s.AgentID, Type: "mem", Value: s.MemUsedPercent, CreatedAt: time.Now()})
					db.Create(&MonitorHistory{AgentID: s.AgentID, Type: "disk", Value: s.DiskUsedPercent, CreatedAt: time.Now()})
				}

				s.Name = node.Name
				s.HideID = node.HideID
				s.SortOrder = node.SortOrder
				s.CountryCode = node.CountryCode
				s.PingTargets = targets
				s.Arch = node.Arch
				s.PendingUpdate = ""

				// [新增] 如需更新，随心跳下发自更新指令
				upd := buildUpdateCommand(node)
				if upd != nil {
					s.PendingUpdate = upd.Version
				}

				cacheMutex.Lock()
				statusCache[s.AgentID] = s
				cacheMutex.Unlock()
				c.JSON(200, AgentResponse{Status: "ok", PingTargets: targets, Update: upd})
			}
		})

		api.GET("/stats", func(c *gin.Context) {
			isAdmin := isAdminSession(c)

			// 1. 获取所有数据库中的节点
			var nodes []Node
			db.Find(&nodes)

			// 先把缓存整份快照出来，再放开读锁。
			// 原实现把 RLock 一直 defer 到函数结束，而序列化上百个节点的 JSON
			// 根本不需要占着这把锁 —— 那期间所有 Agent 的心跳上报都会堵在
			// cacheMutex.Lock() 上，节点越多越明显。
			snapshot := make(map[string]SystemStatus, len(statusCache))
			cacheMutex.RLock()
			for k, v := range statusCache {
				snapshot[k] = v
			}
			cacheMutex.RUnlock()

			res := make(map[string]SystemStatus, len(nodes))
			for _, n := range nodes {
				if n.Denied {
					continue
				}

				// 2. 优先读取缓存中的实时数据
				if v, ok := snapshot[n.AgentID]; ok {
					if !isAdmin {
						v.IP = "Hidden"
					}
					// 确保 DB 中的名称同步
					v.Name = n.Name
					v.HideID = n.HideID
					v.SortOrder = n.SortOrder
					v.CountryCode = n.CountryCode
					v.InstallTime = n.CreatedAt.Unix() // [新增] 注入安装时间戳
					if n.Arch != "" {
						v.Arch = n.Arch
					}
					v.PendingUpdate = n.PendingUpdate
					if !isAdmin {
						v.PendingUpdate = ""
					}
					v.Group = n.Group
					v.Maintenance = n.Maintenance
					v.AlertMuted = n.AlertMuted
					res[n.AgentID] = v
				} else {
					// 3. 如果缓存没有（新建未连接），构造一个“待机”状态
					v := SystemStatus{
						AgentID:       n.AgentID,
						Name:          n.Name,
						HideID:        n.HideID,
						SortOrder:     n.SortOrder,
						CountryCode:   n.CountryCode,
						OS:            "等待接入...",
						LastUpdate:    time.Time{},        // 零值，前端判定为离线
						InstallTime:   n.CreatedAt.Unix(), // [新增] 注入安装时间戳
						Arch:          n.Arch,
						Version:       n.AgentVersion,
						PendingUpdate: n.PendingUpdate,
						Group:         n.Group,
						Maintenance:   n.Maintenance,
						AlertMuted:    n.AlertMuted,
					}
					if !isAdmin {
						v.PendingUpdate = ""
					}
					res[n.AgentID] = v
				}
			}
			c.JSON(200, res)
		})

		api.GET("/history/ping", func(c *gin.Context) {
			id := c.Query("id")
			var history []MonitorHistory
			db.Where("agent_id = ? AND type = 'ping'", id).Order("created_at desc").Limit(100).Find(&history)
			var res []gin.H
			for i := len(history) - 1; i >= 0; i-- {
				res = append(res, gin.H{"time": history[i].CreatedAt, "delay": history[i].Value, "target": history[i].Target})
			}
			c.JSON(200, res)
		})

		api.GET("/history/full", func(c *gin.Context) {
			id := c.Query("id")
			var pings, cpus, mems, disks []MonitorHistory
			db.Where("agent_id = ? AND type = 'ping'", id).Order("created_at desc").Limit(100).Find(&pings)
			db.Where("agent_id = ? AND type = 'cpu'", id).Order("created_at desc").Limit(50).Find(&cpus)
			db.Where("agent_id = ? AND type = 'mem'", id).Order("created_at desc").Limit(50).Find(&mems)
			db.Where("agent_id = ? AND type = 'disk'", id).Order("created_at desc").Limit(50).Find(&disks)

			fmtData := func(list []MonitorHistory) []gin.H {
				// 初始化空切片，避免无数据时序列化成 null
				res := make([]gin.H, 0, len(list))
				for i := len(list) - 1; i >= 0; i-- {
					res = append(res, gin.H{"time": list[i].CreatedAt, "val": list[i].Value, "target": list[i].Target})
				}
				return res
			}

			c.JSON(200, gin.H{
				"ping": fmtData(pings),
				"cpu":  fmtData(cpus),
				"mem":  fmtData(mems),
				"disk": fmtData(disks),
			})
		})

		// Bing 壁纸代理
		api.GET("/bing", func(c *gin.Context) {
			// 1. 定义接口地址 (推荐使用 www 以获得更好的国际连通性，也可改回 cn)
			const bingBase = "https://www.bing.com"
			apiURL := bingBase + "/HPImageArchive.aspx?format=js&idx=0&n=1"

			// 2. 创建请求
			client := &http.Client{Timeout: 5 * time.Second}
			req, err := http.NewRequest("GET", apiURL, nil)
			if err != nil {
				c.Status(500)
				return
			}

			// 3. 关键：伪装 User-Agent，防止被 Bing 拦截
			req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")

			// 4. 发起请求
			resp, err := client.Do(req)
			if err != nil {
				c.Status(500)
				return
			}
			defer resp.Body.Close()

			// 5. 解析并重定向
			var res struct {
				Images []struct {
					Url string `json:"url"`
				} `json:"images"`
			}
			if json.NewDecoder(resp.Body).Decode(&res) == nil && len(res.Images) > 0 {
				// 拼接完整的图片地址并重定向
				c.Redirect(302, bingBase+res.Images[0].Url)
			} else {
				c.Status(500)
			}
		})

		auth := api.Group("/")
		auth.Use(authMiddleware())
		{
			// 系统管理扩展模块（审计 / 账号 / 系统信息 / 数据管理 / 节点批量操作）
			registerAdminRoutes(auth)
			// 告警规则与告警历史
			registerAlertRoutes(auth)

			auth.POST("/settings/create_node", func(c *gin.Context) {
				name := c.PostForm("name")
				if name == "" {
					c.JSON(400, gin.H{"status": "error"})
					return
				}

				b := make([]byte, 3)
				rand.Read(b)
				id := hex.EncodeToString(b)

				db.Create(&Node{AgentID: id, Name: name})

				globalConfig.RLock()
				serverURL := globalConfig.ServerURL
				if serverURL == "" {
					scheme := "http"
					if c.Request.TLS != nil {
						scheme = "https"
					}
					serverURL = scheme + "://" + c.Request.Host
				}
				token := globalConfig.Token
				globalConfig.RUnlock()

				// 命令内含 uname -m 探测，目标机器自行选择 amd64 / arm64 二进制
				cmd := installCommand(serverURL, token, id)

				audit(c, "node_create", id, "新建节点："+name, true)
				c.JSON(200, gin.H{
					"status": "ok",
					"id":     id,
					"cmd":    cmd,
				})
			})

			auth.POST("/settings/token", func(c *gin.Context) {
				t := c.PostForm("token")
				if len(t) < 8 {
					// 通信 Token 是 Agent 身份的唯一凭据，弱 Token 等于把
					// /api/report 直接开放给猜得到的人
					c.String(400, "Token 至少需要 8 位字符")
					return
				}
				saveConfig("token", t)
				globalConfig.Lock()
				globalConfig.Token = t
				globalConfig.Unlock()
				// 改 Token 会让全部已装 Agent 立刻掉线，属于高危动作，必须留痕
				audit(c, "token_change", "", "修改 Agent 通信 Token（全部节点将重新鉴权）", true)
				c.Status(200)
			})
			auth.POST("/settings/url", func(c *gin.Context) {
				u := c.PostForm("url")
				saveConfig("server_url", u)
				globalConfig.Lock()
				globalConfig.ServerURL = u
				globalConfig.Unlock()
				audit(c, "server_url", "", "修改面板公网地址："+u, true)
				c.Status(200)
			})
			auth.POST("/settings/alert", func(c *gin.Context) {
				tk, ch, wh := c.PostForm("token"), c.PostForm("chat"), c.PostForm("webhook")
				wf := c.PostForm("format")
				if wf == "" {
					wf = "generic"
				}
				saveConfig("tg_token", tk)
				saveConfig("tg_chat", ch)
				saveConfig("webhook_url", wh)
				saveConfig("webhook_format", wf)
				globalConfig.Lock()
				globalConfig.TGToken = tk
				globalConfig.TGChatID = ch
				globalConfig.WebhookURL = wh
				globalConfig.WebhookFormat = wf
				globalConfig.Unlock()
				audit(c, "alert_config", "", "更新告警通道配置（Webhook 格式："+wf+"）", true)
				c.Status(200)
			})
			auth.POST("/settings/test_alert", func(c *gin.Context) {
				if n := sendAlert("🔔 测试告警消息\nMonitor 配置成功！"); n == 0 {
					audit(c, "alert_test", "", "发送测试告警失败：未配置任何通知通道", false)
					c.String(400, "尚未配置任何通知通道，请先填写 Telegram 或 Webhook 地址")
					return
				}
				audit(c, "alert_test", "", "发送测试告警", true)
				c.Status(200)
			})
			auth.POST("/settings/update_node", func(c *gin.Context) {
				id := c.PostForm("id")
				name := cleanField(c.PostForm("name"), 64)
				sort, _ := strconv.Atoi(c.PostForm("sort"))
				upd := map[string]interface{}{"name": name, "sort_order": sort}
				// 分组也在这个入口一起提交；留空表示移出分组
				if _, ok := c.GetPostForm("group"); ok {
					upd["group"] = cleanField(c.PostForm("group"), 32)
				}
				db.Model(&Node{}).Where("agent_id=?", id).Updates(upd)
				audit(c, "node_update", id, "更新节点信息："+name, true)
				c.Status(200)
			})

			auth.POST("/settings/theme", func(c *gin.Context) {
				t := c.PostForm("theme")
				if t == "dark" || t == "light" {
					saveConfig("site_theme", t)
					globalConfig.Lock()
					globalConfig.SiteTheme = t
					globalConfig.Unlock()
					c.Status(200)
				} else {
					c.Status(400)
				}
			})

			auth.GET("/settings/get_global_targets", func(c *gin.Context) {
				globalConfig.RLock()
				defer globalConfig.RUnlock()
				c.JSON(200, globalConfig.PingTargets)
			})

			auth.POST("/settings/save_global_targets", func(c *gin.Context) {
				var targets []PingTargetConfig
				if c.ShouldBindJSON(&targets) == nil {
					// 目标会被拼进 Agent 的 ping 命令，先收敛一下长度与非法字符
					for i := range targets {
						targets[i].Target = cleanField(targets[i].Target, 128)
						targets[i].Alias = cleanField(targets[i].Alias, 32)
					}
					b, _ := json.Marshal(targets)
					saveConfig("sys_ping_targets", string(b))
					globalConfig.Lock()
					globalConfig.PingTargets = targets
					globalConfig.Unlock()
					audit(c, "targets_save", "", fmt.Sprintf("保存监控目标，共 %d 项", len(targets)), true)
					c.Status(200)
				}
			})

			auth.POST("/settings/toggle_hide", func(c *gin.Context) {
				var n Node
				db.First(&n, "agent_id=?", c.PostForm("id"))
				db.Model(&n).Update("hide_id", !n.HideID)
				c.Status(200)
			})
			auth.POST("/settings/delete", func(c *gin.Context) {
				id := c.PostForm("id")
				var n Node
				db.First(&n, "agent_id=?", id)
				db.Model(&Node{}).Where("agent_id=?", id).Update("denied", true)
				cacheMutex.Lock()
				delete(statusCache, id)
				cacheMutex.Unlock()
				clearNodeAlertState(id)
				audit(c, "node_delete", id, "删除节点："+n.Name, true)
				c.Status(200)
			})

			// 保存外观配置（增加 padding 参数）
			auth.POST("/settings/appearance", func(c *gin.Context) {
				t := c.PostForm("type")
				u := c.PostForm("url")
				b := c.PostForm("blur")
				o := c.PostForm("opacity")
				p := c.PostForm("padding") // 获取内边距参数

				saveConfig("bg_type", t)
				saveConfig("bg_custom_url", u)
				saveConfig("bg_blur", b)
				saveConfig("card_opacity", o)
				saveConfig("card_padding", p)

				globalConfig.Lock()
				globalConfig.BgType = t
				globalConfig.BgCustomURL = u
				globalConfig.BgBlur, _ = strconv.Atoi(b)
				globalConfig.CardOpacity, _ = strconv.ParseFloat(o, 64)
				globalConfig.CardPadding, _ = strconv.Atoi(p)
				globalConfig.Unlock()
				c.Status(200)
			})

			// ================= 更新模块 =================

			// 检查更新
			auth.GET("/settings/update/check", func(c *gin.Context) {
				rel, err := cachedLatestRelease(false)
				if err != nil {
					c.JSON(200, gin.H{"error": err.Error()})
					return
				}
				c.JSON(200, gin.H{
					"current":      BuildVersion,
					"latest":       rel.Version,
					"has_update":   hasUpdate(BuildVersion, rel.Version),
					"notes":        rel.Notes,
					"url":          rel.URL,
					"published_at": rel.PublishedAt,
				})
			})

			// 更新任务状态
			auth.GET("/settings/update/status", func(c *gin.Context) {
				c.JSON(200, getUpdateState())
			})

			// 面板自更新
			auth.POST("/settings/update/server", func(c *gin.Context) {
				updateState.Lock()
				busy := updateState.Busy
				updateState.Unlock()
				if busy {
					c.JSON(200, gin.H{"error": "已有更新任务正在进行"})
					return
				}
				audit(c, "update_server", "", "触发面板自更新", true)
				startServerUpdate()
				c.Status(200)
			})

			// 同步客户端程序到面板
			auth.POST("/settings/update/agent_sync", func(c *gin.Context) {
				updateState.Lock()
				busy := updateState.Busy
				updateState.Unlock()
				if busy {
					c.JSON(200, gin.H{"error": "已有更新任务正在进行"})
					return
				}
				audit(c, "agent_sync", "", "同步客户端二进制到面板", true)
				startAgentSync()
				c.Status(200)
			})

			// 下发客户端更新指令（id=all 表示全部）
			auth.POST("/settings/update/agent_push", func(c *gin.Context) {
				target := c.PostForm("id")
				n, err := pushAgentUpdate(target)
				if err != nil {
					c.JSON(200, gin.H{"error": err.Error()})
					return
				}
				// 这是最敏感的一条：指令落到节点后会以 root 身份替换并重启进程
				label := target
				if target == "" || target == "all" {
					label = "全部节点"
				}
				audit(c, "agent_push", label, fmt.Sprintf("下发客户端更新，命中 %d 个节点", n), true)
				c.JSON(200, gin.H{"count": n})
			})

			// 更新模块基础信息
			auth.GET("/settings/update/info", func(c *gin.Context) {
				globalConfig.RLock()
				repo, proxy, cmd, bundle := globalConfig.UpdateRepo, globalConfig.UpdateProxy, globalConfig.RestartCmd, globalConfig.AgentBundleVersion
				globalConfig.RUnlock()
				if repo == "" {
					repo = defaultRepo
				}
				c.JSON(200, gin.H{"repo": repo, "proxy": proxy, "cmd": cmd, "bundle": bundle, "archs": cachedArchList()})
			})

			// 保存更新源
			auth.POST("/settings/update/source", func(c *gin.Context) {
				repo := strings.TrimSpace(c.PostForm("repo"))
				proxy := strings.TrimSpace(c.PostForm("proxy"))
				cmd := strings.TrimSpace(c.PostForm("cmd"))
				// 更新源直接决定「推给所有节点的二进制从哪来」，
				// 等同于一条对全部被控服务器的执行通道，不接受任意字符串
				if repo != "" && !validRepo(repo) {
					c.String(400, "仓库格式不正确，应为 owner/repo（如 jinhuaitao/Monitor）")
					return
				}
				if !validProxy(proxy) {
					c.String(400, "加速镜像必须是 https:// 开头的完整地址")
					return
				}
				saveConfig("update_repo", repo)
				saveConfig("update_proxy", proxy)
				saveConfig("restart_cmd", cmd)
				globalConfig.Lock()
				globalConfig.UpdateRepo = repo
				globalConfig.UpdateProxy = proxy
				globalConfig.RestartCmd = cmd
				globalConfig.Unlock()
				audit(c, "update_source", repo,
					fmt.Sprintf("更新源变更为 %s（镜像：%s）", repo, proxy), true)
				c.Status(200)
			})
		}
	}

	// ================= 启动 HTTP 服务 =================
	//
	// 用 http.Server 而不是 gin 的 r.Run()，为了三件事：
	//
	//  ① 超时。裸 http.Server 的四个超时默认全是 0，也就是【永不超时】。
	//     一个只发请求头、迟迟不发完的连接就能永久占住一个连接槽，
	//     几百个这样的连接就足以把面板拖垮 —— 这是最经典的 Slowloris。
	//     gin 的 r.Run() 内部就是这么起的，没有任何超时。
	//
	//  ② 优雅退出。收到 SIGTERM 后等在途请求写完再退，否则 systemctl restart
	//     时正在导出 CSV / 做在线备份的请求会被直接切断，用户只看到连接重置。
	//
	//  ③ 启动失败可诊断。端口被占用时 r.Run() 只把 error 返回给调用方并被丢弃，
	//     进程看起来「在跑」但其实没在监听，systemd 还会一直把它拉起来。
	srv := &http.Server{
		Addr:    ":" + port,
		Handler: r,
		// 只覆盖请求头读取，是防 Slowloris 最关键的一项，10 秒足够任何正常客户端
		ReadHeaderTimeout: 10 * time.Second,
		// 覆盖请求体读取。面板最大的请求体是节点批量操作的 JSON，2 分钟绰绰有余
		ReadTimeout: 2 * time.Minute,
		// 覆盖业务处理 + 响应写出。数据导出与在线备份可能跑几十秒，给宽一些
		WriteTimeout: 10 * time.Minute,
		// 长连接空闲回收。面板前端每 2~5 秒轮询一次，用不到很长的空闲保持
		IdleTimeout: 2 * time.Minute,
		// 面板没有任何需要大请求头的接口，1 MiB 已远超正常值
		MaxHeaderBytes: 1 << 20,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Printf(">> Hub Monitor %s 已启动: http://localhost:%s", displayVersion(), port)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	// Ctrl-C 与 systemctl stop 分别对应 SIGINT / SIGTERM，两者都要接住
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)

	select {
	case err := <-errCh:
		log.Fatalf("[fatal] 监听 %s 失败: %v", srv.Addr, err)
	case sig := <-quit:
		log.Printf(">> 收到信号 %v，正在优雅退出…", sig)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("[warn] 优雅退出超时，强制关闭: %v", err)
		_ = srv.Close()
	}
	// 关库时 SQLite 会做一次 WAL checkpoint，把 -wal 里的数据合并回主文件。
	// 跳过这一步直接拷 monitor.db，拿到的可能是 checkpoint 之前的旧数据。
	if sqlDB, err := db.DB(); err == nil {
		_ = sqlDB.Close()
	}
	log.Println(">> 已退出")
}

// randomToken 生成一个 24 位十六进制随机串，用于首次运行时的 Agent 通信 Token。
//
// 为什么不写死一个「默认 Token」：面板的 /api/report 只认这个 Token，
// 而它是公开仓库里可见的常量 —— 任何知道默认值的人都能往面板里塞伪造节点、
// 或反过来读取下发给节点的 Ping 目标。首次运行随机生成并落库，
// 之后每次启动都从数据库读同一个值，Agent 不会因为重启而掉线。
func randomToken() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("tok-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

func randomEpoch() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	return hex.EncodeToString(b)
}

func loadGlobalConfig() {
	var cfgs []AppConfig
	db.Find(&cfgs)

	// 先全部置为「代码内置默认值」，再用数据库里已有的值覆盖。
	// 这样新增配置项时，老库不需要迁移脚本也能拿到合理默认值。
	globalConfig.Token = ""
	globalConfig.BgType = "default"
	globalConfig.CardOpacity = 0.9   // 默认值
	globalConfig.CardPadding = 10    // 默认值
	globalConfig.SiteTheme = "light" // 默认亮色
	// 告警规则默认值：离线告警开、资源阈值开，冷却 0 表示「只在状态翻转时通知一次」
	globalConfig.AlertEnabled = true
	globalConfig.AlertOffline = true
	globalConfig.AlertOfflineSec = 30
	globalConfig.AlertCooldownMin = 0
	globalConfig.AlertCPU = 90
	globalConfig.AlertMem = 90
	globalConfig.AlertDisk = 90
	globalConfig.AlertKeepDays = 30
	globalConfig.HistoryKeepHours = 24
	globalConfig.AuditKeepDays = 90
	globalConfig.SessionEpoch = ""
	defaultTargets := []PingTargetConfig{{Target: "8.8.8.8:53", Alias: "Google DNS"}}

	seen := make(map[string]bool, len(cfgs))
	for _, c := range cfgs {
		seen[c.Key] = true
		switch c.Key {
		case "token":
			globalConfig.Token = c.Value
		case "server_url":
			globalConfig.ServerURL = c.Value
		case "tg_token":
			globalConfig.TGToken = c.Value
		case "tg_chat":
			globalConfig.TGChatID = c.Value
		case "webhook_url":
			globalConfig.WebhookURL = c.Value
		case "webhook_format":
			globalConfig.WebhookFormat = c.Value
		case "site_theme":
			globalConfig.SiteTheme = c.Value
		case "bg_type":
			globalConfig.BgType = c.Value
		case "bg_custom_url":
			globalConfig.BgCustomURL = c.Value
		case "bg_blur":
			globalConfig.BgBlur, _ = strconv.Atoi(c.Value)
		case "card_opacity":
			globalConfig.CardOpacity, _ = strconv.ParseFloat(c.Value, 64)
		case "card_padding":
			globalConfig.CardPadding, _ = strconv.Atoi(c.Value)
		case "sys_ping_targets":
			json.Unmarshal([]byte(c.Value), &globalConfig.PingTargets)
		// === 更新模块 ===
		case "update_repo":
			globalConfig.UpdateRepo = c.Value
		case "update_proxy":
			globalConfig.UpdateProxy = c.Value
		case "restart_cmd":
			globalConfig.RestartCmd = c.Value
		case "agent_bundle_version":
			globalConfig.AgentBundleVersion = c.Value
		// === 告警规则 ===
		case "alert_enabled":
			globalConfig.AlertEnabled, _ = strconv.ParseBool(c.Value)
		case "alert_offline":
			globalConfig.AlertOffline, _ = strconv.ParseBool(c.Value)
		case "alert_offline_sec":
			globalConfig.AlertOfflineSec, _ = strconv.Atoi(c.Value)
		case "alert_cooldown_min":
			globalConfig.AlertCooldownMin, _ = strconv.Atoi(c.Value)
		case "alert_cpu":
			globalConfig.AlertCPU, _ = strconv.ParseFloat(c.Value, 64)
		case "alert_mem":
			globalConfig.AlertMem, _ = strconv.ParseFloat(c.Value, 64)
		case "alert_disk":
			globalConfig.AlertDisk, _ = strconv.ParseFloat(c.Value, 64)
		case "alert_keep_days":
			globalConfig.AlertKeepDays, _ = strconv.Atoi(c.Value)
		// === 数据保留 ===
		case "history_keep_hours":
			globalConfig.HistoryKeepHours, _ = strconv.Atoi(c.Value)
		case "audit_keep_days":
			globalConfig.AuditKeepDays, _ = strconv.Atoi(c.Value)
		// === 会话版本 ===
		case "session_epoch":
			globalConfig.SessionEpoch = c.Value
		}
	}

	if len(globalConfig.PingTargets) == 0 {
		globalConfig.PingTargets = defaultTargets
	}
	// Token 缺失时立刻生成并落库。
	// 注意不能只写在内存里：否则每次重启都会换一个 Token，全部 Agent 一起掉线。
	if globalConfig.Token == "" {
		globalConfig.Token = randomToken()
		saveConfig("token", globalConfig.Token)
	}
	if globalConfig.SessionEpoch == "" {
		globalConfig.SessionEpoch = randomEpoch()
		saveConfig("session_epoch", globalConfig.SessionEpoch)
	}
	if !seen["sys_ping_targets"] {
		if b, err := json.Marshal(globalConfig.PingTargets); err == nil {
			saveConfig("sys_ping_targets", string(b))
		}
	}
	// 把内置默认值写进库，让「系统管理」里的表单能读到与实际生效一致的值
	defaults := map[string]string{
		"alert_enabled":      strconv.FormatBool(globalConfig.AlertEnabled),
		"alert_offline":      strconv.FormatBool(globalConfig.AlertOffline),
		"alert_offline_sec":  strconv.Itoa(globalConfig.AlertOfflineSec),
		"alert_cooldown_min": strconv.Itoa(globalConfig.AlertCooldownMin),
		"alert_cpu":          strconv.FormatFloat(globalConfig.AlertCPU, 'f', -1, 64),
		"alert_mem":          strconv.FormatFloat(globalConfig.AlertMem, 'f', -1, 64),
		"alert_disk":         strconv.FormatFloat(globalConfig.AlertDisk, 'f', -1, 64),
		"alert_keep_days":    strconv.Itoa(globalConfig.AlertKeepDays),
		"history_keep_hours": strconv.Itoa(globalConfig.HistoryKeepHours),
		"audit_keep_days":    strconv.Itoa(globalConfig.AuditKeepDays),
	}
	for k, v := range defaults {
		if !seen[k] {
			saveConfig(k, v)
		}
	}
}

func saveConfig(k, v string) {
	db.Clauses(clause.OnConflict{UpdateAll: true}).Create(&AppConfig{Key: k, Value: v})
}

func dashboardHandler(c *gin.Context) {
	isAdmin := isAdminSession(c)
	sch := "http://"
	if c.Request.TLS != nil {
		sch = "https://"
	}

	globalConfig.RLock()
	tk := globalConfig.Token
	u := globalConfig.ServerURL
	tgt := globalConfig.TGToken
	tgc := globalConfig.TGChatID
	wh := globalConfig.WebhookURL
	whFmt := globalConfig.WebhookFormat
	bgType := globalConfig.BgType
	bgUrl := globalConfig.BgCustomURL
	bgBlur := globalConfig.BgBlur
	cardOp := globalConfig.CardOpacity
	cardPad := globalConfig.CardPadding
	theme := globalConfig.SiteTheme
	repo := globalConfig.UpdateRepo
	proxy := globalConfig.UpdateProxy
	rCmd := globalConfig.RestartCmd
	bundle := globalConfig.AgentBundleVersion
	globalConfig.RUnlock()

	if !isAdmin {
		tk = ""
		u = ""
		tgt = ""
		tgc = ""
		wh = ""
		repo = ""
		proxy = ""
		rCmd = ""
		bundle = ""
	}
	if repo == "" {
		repo = defaultRepo
	}
	if whFmt == "" {
		whFmt = "generic"
	}

	renderHTML(c, dashboardTmpl, map[string]interface{}{
		"BrowserURL": sch + c.Request.Host, "CustomServerURL": u, "Token": tk, "TGToken": tgt, "TGChatID": tgc, "WebhookURL": wh,
		"WebhookFormat": whFmt, "BcryptCost": bcryptCost,
		"AdminName":   currentUser(c),
		"DownloadURL": "/api/download", "IsAdmin": isAdmin,
		// 安装命令模板：前端用它为已有节点生成命令，与后端 installCommand() 同源
		"InstallTmpl": installCmdTmpl,
		"BgType":      bgType, "BgCustomURL": bgUrl, "BgBlur": bgBlur, "CardOpacity": cardOp,
		"CardPadding": cardPad,
		"Theme":       theme,
		// === 版本更新 ===
		"Version": displayVersion(), "UpdateRepo": repo, "UpdateProxy": proxy,
		"RestartCmd": rCmd, "AgentBundleVersion": bundle,
	})
}

// ================= 会话 Cookie =================
//
// gin-contrib/sessions 的 CookieStore 支持【按请求】覆盖 Cookie 属性：
// session.Options() 写的是本次会话对象上的值，Save() 时直接读它
// （gorilla/sessions 的 CookieStore.Save 用的是 session.Options，
// 而不是 store 的默认值）。于是 Secure 可以逐次按「这次到底是不是 HTTPS」决定。

// secureRequest 判断当前请求是否经由 HTTPS 到达。
//
// 直接终止 TLS 时看 Request.TLS；反向代理后面则看 X-Forwarded-Proto。
// 这里刻意不校验该头的来源：它只用来决定 Cookie 的 Secure 属性，
// 伪造它最多让攻击者自己的浏览器存不下会话 Cookie（等于把自己踢下线），
// 无法据此降级传输或窃取他人会话 —— 因此无需接入可信代理白名单，
// 也就不会与上面 SetTrustedProxies(nil) 的「按真实 IP 限流」目标冲突。
func secureRequest(c *gin.Context) bool {
	if c.Request.TLS != nil {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(c.GetHeader("X-Forwarded-Proto")), "https")
}

// sessionOptions 本次请求使用的 Cookie 属性。
//
// Secure 只在 HTTPS 下打开：面板大量部署在纯 HTTP 内网，无条件加 Secure
// 会让浏览器直接丢弃会话 Cookie，表现为「密码明明对了、登录也成功了，
// 但立刻又跳回登录页」这种极难自查的现象。
func sessionOptions(c *gin.Context) sessions.Options {
	return sessions.Options{
		Path:     "/",
		MaxAge:   3600 * 24,
		HttpOnly: true,
		Secure:   secureRequest(c),
		SameSite: http.SameSiteStrictMode,
	}
}

// saveSession 保存会话并统一套用 Cookie 属性。
// 写失败只记日志：会话存不下来不该把业务接口一起带崩。
func saveSession(c *gin.Context, s sessions.Session) {
	s.Options(sessionOptions(c))
	if err := s.Save(); err != nil {
		log.Printf("[session] 保存会话失败: %v", err)
	}
}

// sessionKeyFromEnv 取得会话签名密钥。
//
// 未设置 SESSION_KEY 时每次启动随机生成：好处是「重启即让全部会话失效」，
// 代价是重启后需要重新登录。设置了则跨重启保持登录态 —— 因此必须校验长度，
// 否则用户图省事填个 "123456"，在线爆破 Cookie 签名会比爆破密码容易得多。
func sessionKeyFromEnv() ([]byte, error) {
	if v := strings.TrimSpace(os.Getenv("SESSION_KEY")); v != "" {
		if len(v) < 32 {
			return nil, fmt.Errorf(
				"SESSION_KEY 至少需要 32 个字符（当前 %d 个）；留空则由面板每次启动随机生成", len(v))
		}
		return []byte(v), nil
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		// crypto/rand 失败说明系统熵源异常，此时生成的密钥不可信；
		// 拿它签名等于把管理员会话拱手让人，所以拒绝启动而不是降级。
		return nil, fmt.Errorf("生成会话密钥失败（系统随机源不可用）: %w", err)
	}
	return key, nil
}

// securityHeaders 统一注入一批「只做加法」的安全响应头。
//
// 选的都是不会破坏页面的项：
//   - nosniff           阻止浏览器把 JSON / 文本响应猜成脚本执行
//   - X-Frame-Options   防点击劫持（frame-ancestors 覆盖现代浏览器）
//   - Referrer-Policy   跳转外部（Bing 壁纸、自定义背景图）时不带出面板完整地址
//   - X-Robots-Tag      仪表盘默认对未登录访客可见，必须明确要求搜索引擎不要收录
//
// 这里刻意【不】下发完整的 Content-Security-Policy：前端依赖内联 script/style，
// 图表与字体来自 CDN，背景图还允许用户填任意 URL —— 一份写死的 CSP
// 极易把界面打坏，而「打坏 CSP」比「没有 CSP」更糟，因为没人会立刻发现功能没了。
// 只保留不依赖资源白名单的三条指令：不限制任何正常加载，但能挡住插件嵌入、
// <base> 劫持与跨站 iframe 嵌套。
func securityHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		h := c.Writer.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "SAMEORIGIN")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Robots-Tag", "noindex, nofollow")
		h.Set("Permissions-Policy", "geolocation=(), camera=(), microphone=()")
		h.Set("Content-Security-Policy", "frame-ancestors 'self'; base-uri 'none'; object-src 'none'")
		if secureRequest(c) {
			// HSTS 一旦被浏览器记住就无法撤销，所以只在确认走了 HTTPS 时才发，
			// 免得把纯 HTTP 内网部署的浏览器直接锁死在 https 上。
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
		c.Next()
	}
}

// limitBodySize 给请求体设硬上限。
//
// Go 的 JSON 解码会把整个 body 读进内存，而面板没有任何接口需要大请求体
// （最大的是节点批量操作的 JSON，几 KB 量级）。没有上限时，
// 一个几 GB 的 POST 就能把进程内存打满。
func limitBodySize(max int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Body != nil {
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, max)
		}
		c.Next()
	}
}

func authMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		var cnt int64
		db.Model(&User{}).Count(&cnt)
		if cnt == 0 {
			c.Redirect(302, "/setup")
			c.Abort()
			return
		}
		if !isAdminSession(c) {
			// 会话失效（未登录 / Cookie 过期 / 改密码后 epoch 变更）一律回登录页。
			// 这里顺手把脏 Cookie 清掉，避免浏览器带着一份永远无效的会话反复重试。
			s := sessions.Default(c)
			if s.Get("user") != nil {
				s.Clear()
				saveSession(c, s)
			}
			c.Redirect(302, "/login")
			c.Abort()
			return
		}
		c.Next()
	}
}

// ================= 密码存储 =================
//
// 早期版本用的是「随机盐 + 单轮 SHA-256」。它的问题不在于加没加盐，
// 而在于 SHA-256 是为速度设计的：一张消费级显卡每秒能算上百亿次，
// 一旦数据库泄露（备份文件、误挂载的卷），口令基本等于明文。
//
// 现在改用 bcrypt：内置盐、可调工作因子，天生抗暴力破解。
// 为兼容老库，checkPwd 仍能识别旧的 salt$sha256 格式，
// 并在登录成功的那一刻原地升级为 bcrypt —— 用户不需要重置密码。

// bcryptCost 12 在现代 CPU 上单次约 200~300ms：
// 登录体验几乎无感，但把离线爆破的成本抬高了几个数量级。
const bcryptCost = 12

// 安全增强: 密码哈希（bcrypt）
func hashPwd(password string) string {
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		// 理论上不会失败；真失败时退回旧格式，保证功能不中断
		salt := make([]byte, 16)
		rand.Read(salt)
		sum := sha256.Sum256(append(salt, []byte(password)...))
		return hex.EncodeToString(salt) + "$" + hex.EncodeToString(sum[:])
	}
	return string(h)
}

// checkPwd 校验密码；第二个返回值表示「该哈希是旧格式，应升级」
func checkPwd(password, stored string) bool {
	ok, _ := checkPwdUpgrade(password, stored)
	return ok
}

func checkPwdUpgrade(password, stored string) (bool, bool) {
	if strings.HasPrefix(stored, "$2a$") || strings.HasPrefix(stored, "$2b$") || strings.HasPrefix(stored, "$2y$") {
		return bcrypt.CompareHashAndPassword([]byte(stored), []byte(password)) == nil, false
	}
	// 旧格式：salt$sha256
	parts := strings.Split(stored, "$")
	if len(parts) != 2 {
		return false, false
	}
	salt, err1 := hex.DecodeString(parts[0])
	expectedHash, err2 := hex.DecodeString(parts[1])
	if err1 != nil || err2 != nil {
		return false, false
	}
	actualHash := sha256.Sum256(append(salt, []byte(password)...))
	if subtle.ConstantTimeCompare(expectedHash, actualHash[:]) != 1 {
		return false, false
	}
	return true, true
}

// cleanField 剔除控制字符并截断到指定长度
func cleanField(v string, max int) string {
	v = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, v)
	if rs := []rune(v); len(rs) > max {
		v = string(rs[:max])
	}
	return strings.TrimSpace(v)
}

// cleanMultiline 与 cleanField 同样的收敛逻辑，但保留换行。
//
// 为什么需要单独一个：告警文案天然是多行的（「🔴 节点离线\n名称：…\nID：…」），
// 前端拿到之后会把 \n 换成 " · " 再渲染成一行。而 cleanField 会把 \n 当作
// 控制字符直接删掉 —— 于是落到告警历史里的就变成
// 「🔴 节点离线名称：hk-01ID：abc」这样一整串粘死的文字，
// 页面上那个分隔符永远不会出现，可读性极差。
func cleanMultiline(v string, max int) string {
	// 先统一换行符：否则 \r\n 中的 \r 会被当普通控制字符删掉，
	// 留下孤立的 \n，看上去没问题但长度计算会偏。
	v = strings.ReplaceAll(v, "\r\n", "\n")
	v = strings.ReplaceAll(v, "\r", "\n")
	v = strings.Map(func(r rune) rune {
		if r == '\n' {
			return r
		}
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, v)
	if rs := []rune(v); len(rs) > max {
		v = string(rs[:max])
	}
	return strings.TrimSpace(v)
}

// tokenEqual 以常量时间比较 Agent 通信 Token。
//
// 普通的 != 会在第一个不同的字节处提前返回，理论上可以按响应耗时逐字节
// 猜出 Token。网络抖动远大于单字节比较的差异，现实中很难利用，
// 但 Token 是全部节点身份的唯一凭据 —— 没有理由不为它换成常量时间比较，
// 何况 crypto/subtle 本来就已经为了校验旧口令哈希而引入了。
func tokenEqual(got, want string) bool {
	if got == "" || want == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// sanitizeReport 收敛 Agent 上报的文本字段。
//
// 这些字段（os / ip / version / arch / agent_id）会原样出现在面板界面上，
// 属于不可信输入：任何拿到 Token 的人都能往 /api/report 里塞任意内容。
// 前端已经做了转义（那才是防 XSS 的正解），这里再限长 + 去控制字符，
// 避免超长内容把卡片布局撑坏，也顺带挡住换行注入之类的花样。
func sanitizeReport(s *SystemStatus) {
	s.AgentID = cleanField(s.AgentID, 64)
	s.OS = cleanField(s.OS, 64)
	s.IP = cleanField(s.IP, 64)
	s.Version = cleanField(s.Version, 32)
	s.Arch = cleanField(s.Arch, 16)
	s.CPUModel = cleanField(s.CPUModel, 96)

	// 容量字段同样不可信：一个被篡改的 Agent 可以上报 2^64-1，
	// 前端会把它渲染成天文数字。超过 1 PiB 的一律视为无效置零，
	// 前端据此显示「—」而不是一个假数字。
	const maxSaneBytes = uint64(1) << 50 // 1 PiB
	if s.MemTotal > maxSaneBytes {
		s.MemTotal = 0
	}
	if s.DiskTotal > maxSaneBytes {
		s.DiskTotal = 0
	}

	// 百分比字段同样不可信。前端直接拿它们当进度条宽度（width: 137%）与
	// 大号数字渲染，一个 1e9 或负值就能把整张卡片顶坏。
	// 合法值域只有 0~100，越界一律夹到边界（而不是置零：置零会让一台
	// 真实负载 100% 的机器显示成空闲，比显示 100% 更容易误导）。
	s.CPUUsage = clampPercent(s.CPUUsage)
	s.MemUsedPercent = clampPercent(s.MemUsedPercent)
	s.DiskUsedPercent = clampPercent(s.DiskUsedPercent)
}

// clampPercent 把百分比夹到 [0,100]。
// JSON 没有 NaN 字面量，所以不需要额外处理 NaN。
func clampPercent(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

// ================= 归属地查询 =================
//
// 这是面板唯一访问外部服务的地方：拿节点 IP 换国家代码，用于在卡片上显示旗帜。
// 三个必须收口的点：
//
//  1. 必须带超时。原实现用的是 http.Get（等价 http.DefaultClient），
//     它的 Timeout 是 0 —— 也就是永不超时。对方只要挂住连接，这个 goroutine
//     就永久泄漏，而且节点越多泄漏越多。
//
//  2. 拼进 URL 之前必须确认它是合法 IP。s.IP 来自 Agent 上报，sanitizeReport
//     只做了去控制字符与限长，没有做格式校验 —— 不校验就等于把一段任意
//     字符串拼进了请求路径。
//
//  3. 同一个节点只允许一个在途查询。原实现是每次心跳都起一个 goroutine，
//     而 ip-api.com 免费版有频率限制，节点一多就会互相挤成 429，
//     结果谁都查不到、还每 5 秒重试一轮。
//
// 注：免费接口只有 http://，没有 https。这里换来的只是一个国家代码，
// 且结果只用于展示，不接受 https 之外的加固手段。
var (
	geoClient = &http.Client{Timeout: 5 * time.Second}

	geoInflight struct {
		sync.Mutex
		m map[string]bool
	}
)

func resolveCountryAsync(agentID, ip string) {
	ip = strings.TrimSpace(ip)
	if net.ParseIP(ip) == nil {
		return
	}

	geoInflight.Lock()
	if geoInflight.m == nil {
		geoInflight.m = make(map[string]bool)
	}
	if geoInflight.m[agentID] {
		geoInflight.Unlock()
		return
	}
	geoInflight.m[agentID] = true
	geoInflight.Unlock()

	defer func() {
		geoInflight.Lock()
		delete(geoInflight.m, agentID)
		geoInflight.Unlock()
	}()

	resp, err := geoClient.Get("http://ip-api.com/json/" + url.PathEscape(ip))
	if err != nil {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return
	}

	var res struct {
		CountryCode string `json:"countryCode"`
	}
	// 限长读取：响应内容由对方决定，不能让一个超长 body 把内存吃掉
	if json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&res) != nil {
		return
	}
	if len(res.CountryCode) != 2 {
		return
	}
	db.Model(&Node{}).Where("agent_id = ?", agentID).
		Update("country_code", strings.ToUpper(res.CountryCode))
}

// ================= Agent =================

func runAgent(server, token, id string) {
	fmt.Printf("Agent -> %s (ID:%s)\n", server, id)
	url := fmt.Sprintf("%s/api/report", server) // 移除 URL 参数
	client := &http.Client{Timeout: 5 * time.Second}

	hostInfo, _ := host.Info()
	osInfo := fmt.Sprintf("%s %s", hostInfo.Platform, hostInfo.PlatformVersion)

	// CPU 型号在进程生命周期内不会变，循环外只读一次。
	// cpu.Info() 要解析 /proc/cpuinfo，塞进 2 秒一次的循环里纯属浪费。
	cpuModel := ""
	if infos, err := cpu.Info(); err == nil && len(infos) > 0 {
		cpuModel = strings.TrimSpace(infos[0].ModelName)
	}

	var lastIn, lastOut uint64
	var lastTime time.Time
	currentTargets := []PingTargetConfig{{Target: "8.8.8.8:53"}}

	// Ping 频率控制变量
	const pingInterval = 20 * time.Second

	var latestPingResults = make(map[string]int64) // 缓存 Ping 结果
	var lastPingTime time.Time                     // 上次 Ping 的时间

	// 自更新：记录失败版本，避免反复重试
	updateFails := make(map[string]time.Time)

	for {
		// 1. 获取系统基础数据 (保持每 2秒 获取一次)
		cIdx, _ := cpu.Percent(0, false)
		vm, _ := mem.VirtualMemory()
		du, _ := disk.Usage("/")
		nio, _ := gonet.IOCounters(false)

		cVal := 0.0
		if len(cIdx) > 0 {
			cVal = cIdx[0]
		}
		// gopsutil 在采集失败时返回的是 nil 指针而不是零值结构体，
		// 直接 vm.UsedPercent 会 panic 掉整个 Agent（且是静默的，
		// 面板上只表现为"节点突然离线"）。这里显式判空。
		memUsedPct, memTotal := 0.0, uint64(0)
		if vm != nil {
			memUsedPct, memTotal = vm.UsedPercent, vm.Total
		}
		diskUsedPct, diskTotal := 0.0, uint64(0)
		if du != nil {
			diskUsedPct, diskTotal = du.UsedPercent, du.Total
		}
		curIn, curOut := uint64(0), uint64(0)
		if len(nio) > 0 {
			curIn = nio[0].BytesRecv
			curOut = nio[0].BytesSent
		}

		now := time.Now()
		spIn, spOut := uint64(0), uint64(0)
		if !lastTime.IsZero() {
			d := now.Sub(lastTime).Seconds()
			if d > 0 {
				if curIn >= lastIn {
					spIn = uint64(float64(curIn-lastIn) / d)
				}
				if curOut >= lastOut {
					spOut = uint64(float64(curOut-lastOut) / d)
				}
			}
		}
		lastIn, lastOut, lastTime = curIn, curOut, now

		// Ping 逻辑带时间锁
		if time.Since(lastPingTime) >= pingInterval {
			tempResults := make(map[string]int64)
			var wg sync.WaitGroup
			var mu sync.Mutex

			for _, t := range currentTargets {
				wg.Add(1)
				go func(target string) {
					defer wg.Done()
					var ms int64
					// 区分 TCP Ping (带冒号) 和 ICMP Ping
					if strings.Contains(target, ":") {
						start := time.Now()
						if conn, err := net.DialTimeout("tcp", target, 2*time.Second); err == nil {
							ms = time.Since(start).Milliseconds()
							conn.Close()
						}
					} else {
						ms = execPing(target)
					}
					// 只有成功才记录
					if ms > 0 {
						mu.Lock()
						tempResults[target] = ms
						mu.Unlock()
					}
				}(t.Target)
			}
			wg.Wait()

			latestPingResults = tempResults
			lastPingTime = time.Now()
		}

		uptime, _ := host.Uptime()

		s := SystemStatus{
			AgentID: id, OS: osInfo, Uptime: uptime,
			CPUUsage: cVal, MemUsedPercent: memUsedPct, DiskUsedPercent: diskUsedPct,
			NetInSpeed: spIn, NetOutSpeed: spOut, NetTotalIn: curIn, NetTotalOut: curOut,
			PingResults: latestPingResults,
			Version:     BuildVersion,   // [新增] 上报自身版本，供面板判断是否需要更新
			Arch:        runtime.GOARCH, // [新增] 上报架构，用于分发对应二进制
			CPUModel:    cpuModel,       // [新增] CPU 型号，供面板展示硬件规格
			MemTotal:    memTotal,       // [新增] 物理内存总量
			DiskTotal:   diskTotal,      // [新增] 根分区总容量
		}

		d, _ := json.Marshal(s)
		req, _ := http.NewRequest("POST", url, bytes.NewBuffer(d))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", token)

		resp, err := client.Do(req)

		if err == nil {
			// 限长读取：响应来自面板，正常只有几百字节；
			// 万一对面被换成了别的服务，也不至于把 Agent 的内存吃光。
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
			resp.Body.Close()
			var serverResp AgentResponse
			if json.Unmarshal(body, &serverResp) == nil {
				if serverResp.Status == "stop" {
					fmt.Println(">> 收到停止指令，Agent 正在停止...")
					uninstallAgent()
					return
				}
				if len(serverResp.PingTargets) > 0 {
					newStr, _ := json.Marshal(serverResp.PingTargets)
					oldStr, _ := json.Marshal(currentTargets)
					if string(newStr) != string(oldStr) {
						fmt.Printf("Config Update: Targets -> %s\n", newStr)
						currentTargets = serverResp.PingTargets
						lastPingTime = time.Time{}
					}
				}

				// [新增] 处理面板下发的自更新指令
				if serverResp.Update != nil && serverResp.Update.URL != "" {
					upd := serverResp.Update
					if upd.Version != "" && upd.Version != BuildVersion && canRetryUpdate(updateFails, upd.Version) {
						fmt.Printf(">> 收到更新指令: %s => %s\n", BuildVersion, upd.Version)
						if err := doAgentUpdate(server, token, *upd); err != nil {
							fmt.Println("⚠️ 更新失败:", err)
							updateFails[upd.Version] = time.Now()
						}
						// 成功时 doAgentUpdate 内部会替换自身并退出进程
					}
				}
			}
		}

		time.Sleep(5 * time.Second)
	}
}

// 修复后的自动卸载逻辑
func uninstallAgent() {
	fmt.Println(">> 收到卸载指令，开始执行自我销毁...")

	// 1. 获取当前二进制文件的绝对路径
	binPath, err := filepath.Abs(os.Args[0])
	if err != nil {
		fmt.Println("错误: 无法获取文件路径，将尝试使用默认路径")
		binPath = "./monitor"
	}

	// 2. 根据系统类型清理服务配置
	if _, err := os.Stat("/etc/alpine-release"); err == nil {
		// ================= Alpine (OpenRC) 逻辑 =================
		fmt.Println("-> 检测到 Alpine 系统，正在清理 OpenRC 服务...")

		exec.Command("rc-update", "del", "monitor").Run()
		if err := os.Remove("/etc/init.d/monitor"); err == nil {
			fmt.Println("✅ 服务脚本已删除 (/etc/init.d/monitor)")
		} else {
			fmt.Printf("⚠️ 删除服务脚本失败 (可能已不存在): %v\n", err)
		}

	} else {
		// ================= Debian/Ubuntu/CentOS (Systemd) 逻辑 =================
		fmt.Println("-> 检测到 Systemd 系统，正在清理服务...")

		exec.Command("systemctl", "disable", "monitor").Run()
		serviceFile := "/etc/systemd/system/monitor.service"
		if err := os.Remove(serviceFile); err == nil {
			fmt.Println("✅ 服务文件已删除 (/etc/systemd/system/monitor.service)")
		} else {
			fmt.Printf("⚠️ 删除服务文件失败 (可能已不存在): %v\n", err)
		}
		exec.Command("systemctl", "daemon-reload").Run()
	}

	// 3. 删除 Agent 二进制文件自身
	if err := os.Remove(binPath); err == nil {
		fmt.Println("✅ Agent 自身文件已删除")
	} else {
		fmt.Printf("⚠️ 删除自身文件失败: %v\n", err)
		// 备用方案：尝试重命名
		os.Rename(binPath, binPath+".del")
	}

	// 4. 退出进程，由系统守护进程尝试重启失败从而彻底终止
	fmt.Println("👋 卸载完成，再见！")
	os.Exit(0)
}

func execPing(ip string) int64 {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("ping", "-n", "1", "-w", "1000", ip)
	} else {
		cmd = exec.Command("ping", "-c", "1", "-W", "1", ip)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return 0
	}
	re := regexp.MustCompile(`(?i)(?:time|时间)[=<]([\d\.]+)`)
	matches := re.FindStringSubmatch(string(out))
	if len(matches) > 1 {
		val, _ := strconv.ParseFloat(matches[1], 64)
		return int64(val)
	}
	return 0
}
