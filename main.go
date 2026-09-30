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
var dbPath = "monitor.db"

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

// ================= 上报链路的资源上限 =================
//
// /api/report 是唯一一个「外部进程可以反复调用、且每次都会写库」的入口。
// 下面这些常量把它的成本钉死在一个可预期的范围内：拿不到上限的话，
// 单个持有 Token 的客户端就足以把面板的内存或磁盘吃干净。
const (
	// maxReportBytes 单次心跳报文上限。正常心跳只有几 KB。
	maxReportBytes = 256 << 10
	// maxPingTargetsPerReport 单次心跳携带的 ping 目标数上限。
	maxPingTargetsPerReport = 64
	// maxPingDelayMs 单条延迟的合理上限（5 分钟），超出视为异常值丢弃。
	maxPingDelayMs = 5 * 60 * 1000
	// maxAutoRegisterNodes 允许自动注册的节点总数上限。
	maxAutoRegisterNodes = 5000
)

// geoClient 专用于地理位置查询的客户端。
//
// 原实现用的是 http.Get，也就是 http.DefaultClient —— 它【没有超时】。
// 第三方接口一旦挂住，这个 goroutine 会永久泄漏；而查询是每次心跳都可能
// 触发的，节点多的时候会稳定地一秒钟泄漏一个 goroutine。
var geoClient = &http.Client{Timeout: 5 * time.Second}

// lookupCountryCode 异步补全节点的国家代码。
//
// 说明：ip-api.com 的免费接口只支持明文 HTTP（HTTPS 需付费），所以这里
// 仍是 http —— 也就是说节点 IP 会经过一段明文链路。要彻底解决只能换
// 服务商或自建 IP 库，属于产品决策，不在本轮改动范围内。
// 本函数负责的是另外三件事：加超时、只对合法 IP 发起查询（避免把任意
// 字符串拼进 URL 路径）、限制响应体大小。
func lookupCountryCode(agentID, ip string) {
	if net.ParseIP(strings.TrimSpace(ip)) == nil {
		return
	}
	resp, err := geoClient.Get("http://ip-api.com/json/" + url.PathEscape(ip) + "?fields=countryCode")
	if err != nil {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return
	}
	var res struct {
		CountryCode string `json:"countryCode"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&res) != nil {
		return
	}
	// 只接受标准的两位国家代码，避免把第三方返回的任意内容写进库里
	cc := strings.ToUpper(strings.TrimSpace(res.CountryCode))
	if len(cc) != 2 {
		return
	}
	db.Model(&Node{}).Where("agent_id = ?", agentID).Update("country_code", cc)
}

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
	ID      uint   `gorm:"primaryKey"`
	AgentID string `gorm:"index;index:idx_hist_lookup,priority:1"`
	Type    string `gorm:"index;index:idx_hist_lookup,priority:2"`
	Target  string
	Value   float64
	// 复合索引 (agent_id, type, created_at)：历史查询的固定形态是
	// 「某节点 + 某类型 + 按时间倒序取 N 条」。只有单列索引时 SQLite
	// 只能先用 agent_id 选出该节点的全部行再排序，历史表一大就很慢；
	// 三个字段的复合索引可以让它直接走索引倒序扫描。
	CreatedAt time.Time `gorm:"index;index:idx_hist_lookup,priority:3"`
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

// ================= 主程序 =================

func main() {
	mode := flag.String("mode", "server", "Mode")
	port := flag.String("port", "8080", "Port")
	sAddr := flag.String("server", "http://localhost:8080", "Agent Server")
	tkn := flag.String("token", "", "Agent Token")
	aid := flag.String("id", "", "Agent ID")

	flag.Parse()

	if *mode == "agent" {
		if *tkn == "" || *aid == "" {
			panic("Agent need -token and -id")
		}
		runAgent(*sAddr, *tkn, *aid)
	} else if *mode == "install" {
		installAgent(*sAddr, *tkn, *aid)
	} else if *mode == "version" || *mode == "-v" {
		fmt.Printf("Hub Monitor %s\ncommit: %s\nbuilt:  %s\nplatform: %s/%s\n",
			displayVersion(), BuildCommit, BuildTime, runtime.GOOS, runtime.GOARCH)
	} else {
		runServer(*port)
	}
}

// ================= 安装逻辑 =================

func installAgent(server, token, id string) {
	fmt.Println(">> 正在安装监控 Agent...")
	binPath, err := filepath.Abs(os.Args[0])
	if err != nil {
		fmt.Println("错误: 无法获取文件路径")
		return
	}
	if _, err := os.Stat("/etc/alpine-release"); err == nil {
		installOpenRC(binPath, server, token, id)
	} else {
		installSystemd(binPath, server, token, id)
	}
}

func installSystemd(binPath, server, token, id string) {
	fmt.Println("-> 检测到 Systemd 系统")
	// ExecStart 的每个参数都单独加引号：路径里出现空格（/opt/my monitor/monitor）
	// 会让 systemd 把参数切错，服务起不来却只报 "No such file or directory"。
	// 换行符必须在这里挡住 —— 它能直接往 unit 文件里插入新的指令行
	//（例如 User=root / ExecStartPre=...），属于写文件层面的注入。
	if err := rejectUnitInjection(server, token, id); err != nil {
		fmt.Println("❌ 安装参数非法：", err)
		return
	}
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
`, systemdQuote(binPath), systemdQuote(server), systemdQuote(token), systemdQuote(id))
	if err := os.WriteFile("/etc/systemd/system/monitor.service", []byte(serviceContent), 0644); err != nil {
		fmt.Println("❌ 写入 systemd 服务文件失败：", err)
		return
	}
	exec.Command("systemctl", "daemon-reload").Run()
	exec.Command("systemctl", "enable", "monitor").Run()
	exec.Command("systemctl", "restart", "monitor").Run()
	fmt.Println("✅ 安装成功! 服务已启动并设置开机自启。")
}

func installOpenRC(binPath, server, token, id string) {
	fmt.Println("-> 检测到 Alpine (OpenRC) 系统")
	if err := rejectUnitInjection(server, token, id); err != nil {
		fmt.Println("❌ 安装参数非法：", err)
		return
	}
	// OpenRC 的 command_args 由 shell 解析，因此用单引号包住每个值；
	// 同样先挡掉换行与单引号，避免参数逃逸成新的一行配置。
	scriptContent := fmt.Sprintf(`#!/sbin/openrc-run
name="monitor"
command="%s"
command_args="-mode agent -server %s -token %s -id %s"
command_background=true
pidfile="/run/monitor.pid"
`, shellQuote(binPath), shellQuote(server), shellQuote(token), shellQuote(id))
	if err := os.WriteFile("/etc/init.d/monitor", []byte(scriptContent), 0755); err != nil {
		fmt.Println("❌ 写入 OpenRC 服务脚本失败：", err)
		return
	}
	exec.Command("rc-update", "add", "monitor").Run()
	exec.Command("rc-service", "monitor", "restart").Run()
	fmt.Println("✅ 安装成功! 服务已启动并设置开机自启。")
}

// rejectUnitInjection 拒绝会破坏服务配置文件的参数。
//
// -server / -token / -id 都会原样写进 systemd unit 或 OpenRC 脚本。
// 其中 token 来自面板配置（管理员可自由填写），换行符能插入新的 unit 指令，
// 单引号/双引号能在 OpenRC 的 command_args 里逃逸出来执行任意命令。
// 这些值本来就只允许出现在 URL、主机名、十六进制 ID 与 Token 里，
// 直接把可疑字符拒掉，比事后转义更不容易出错。
func rejectUnitInjection(values ...string) error {
	for _, v := range values {
		if strings.ContainsAny(v, "\r\n") {
			return errors.New("参数中不能包含换行符")
		}
		if strings.ContainsAny(v, `"'`) {
			return errors.New("参数中不能包含引号")
		}
		if strings.Contains(v, "\\") {
			return errors.New("参数中不能包含反斜杠")
		}
	}
	return nil
}

// systemdQuote 按 systemd 的规则给 ExecStart 参数加引号。
// systemd 只认双引号，内部的反斜杠与双引号需要转义。
func systemdQuote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
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

// ================= 安全响应头 =================
//
// 面板把 HTML 内联在单文件二进制里，页面自带 <script> 与 onclick 处理器，
// 因此 CSP 无法彻底去掉 script-src 'unsafe-inline'（那是另一轮改造）。
// 但下面几条依然有实效，尤其是 connect-src：
//
//   - frame-ancestors 'none' / X-Frame-Options：挡住点击劫持。管理员在别的
//     页面里被套一层透明 iframe，「改更新源」这种按钮点一下就生效。
//   - connect-src 'self'：即使真的被注入脚本，fetch/XHR 也发不出去 ——
//     令牌外带这条最关键的路径被切断。
//   - form-action 'self' / base-uri 'none'：挡住表单外发与 <base> 劫持。
//   - nosniff / Referrer-Policy：常规硬化。
const contentSecurityPolicy = "default-src 'self'; " +
	"script-src 'self' 'unsafe-inline' https://cdn.jsdelivr.net; " +
	"style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; " +
	"font-src 'self' data: https://fonts.gstatic.com; " +
	"img-src 'self' data: https:; " +
	"connect-src 'self'; " +
	"object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'"

func securityHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		h := c.Writer.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Permissions-Policy", "geolocation=(), microphone=(), camera=()")
		// HSTS 只在确认走 HTTPS 时才下发。
		// 面板常以 http://IP:8080 直连，对 HTTP 站点发 HSTS 会把浏览器
		// 永久锁在打不开的 https:// 上（除非用户手动清 HSTS 缓存）。
		if c.Request.TLS != nil || strings.EqualFold(c.GetHeader("X-Forwarded-Proto"), "https") {
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		c.Next()
	}
}

// loadSessionSecret 返回会话签名密钥，并保证它在重启之间保持不变。
//
// 早期实现是「每次启动随机生成 32 字节」：进程一重启，所有设备上的 Cookie
// 立刻失效。systemd 配的是 Restart=always，于是一次崩溃重启就会静默登出
// 全部管理员 —— 而且日志里没有任何线索。
//
// 现在落库到 AppConfig：密钥仍由 crypto/rand 生成（不写死在代码里），
// 只是被持久化下来。SESSION_KEY 环境变量优先级最高，便于多实例共享同一密钥。
func loadSessionSecret() []byte {
	if envKey := strings.TrimSpace(os.Getenv("SESSION_KEY")); envKey != "" {
		return []byte(envKey)
	}
	var cfg AppConfig
	if err := db.Where("key = ?", "session_secret").First(&cfg).Error; err == nil && len(cfg.Value) >= 32 {
		return []byte(cfg.Value)
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		// 熵源坏了就没有安全可言，直接拒绝启动而不是用可预测的密钥跑起来
		log.Fatalf("无法生成会话密钥（系统熵源不可用）: %v", err)
	}
	secret := hex.EncodeToString(b)
	saveConfig("session_secret", secret)
	return []byte(secret)
}

// ================= 服务端 =================

func runServer(port string) {
	var err error
	dbPath = "monitor.db"
	db, err = gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		panic(err)
	}

	db.AutoMigrate(&User{}, &AppConfig{}, &Node{}, &MonitorHistory{}, &AuditLog{}, &AlertEvent{})
	loadGlobalConfig()
	go monitorAlerts()
	go cleanupMaintenance()

	gin.SetMode(gin.ReleaseMode)
	r := gin.Default()
	r.Use(securityHeaders())

	// 安全增强：不让 gin 自己决定采信哪个转发头。
	//
	// gin 默认把 X-Forwarded-For 当作可信来源，ClientIP() 因此可被请求方
	// 随意伪造 —— 对「登录限流」这种按 IP 计数的防护来说等于形同虚设。
	// 这里关掉 gin 的内建逻辑，改由 clientIP() 统一裁决：
	// 对端不在 TRUSTED_PROXIES 内就完全忽略转发头，在列表内才从右往左
	// 解析出第一个非可信地址（Nginx 的 $proxy_add_x_forwarded_for 是
	// 追加语义，真实地址在右边，取最左值等于把伪造权还给攻击者）。
	_ = r.SetTrustedProxies(nil)

	// PWA：manifest / Service Worker / 运行时绘制的图标 / 离线页
	registerPWARoutes(r)

	// 会话签名密钥：持久化到数据库，重启不再把所有人踢下线。
	sessionKey := loadSessionSecret()
	store := cookie.NewStore(sessionKey)
	// Cookie 属性：HttpOnly 挡 XSS 读 Cookie，SameSite=Strict 挡 CSRF。
	// Secure 默认不开 —— 面板通常以 http://IP:8080 直连，强行开 Secure
	// 会让浏览器直接丢弃 Cookie，表现为「登录后立刻又回到登录页」。
	// 反向代理终止 TLS 的部署可设 COOKIE_SECURE=1 打开。
	store.Options(sessions.Options{
		Path:     "/",
		MaxAge:   3600 * 24,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   os.Getenv("COOKIE_SECURE") == "1",
	})
	r.Use(sessions.Sessions("mysession", store))

	// 下载程序本体：按 ?arch= 分发对应架构的 Agent 二进制。
	// 不带 arch 参数时保持旧行为（分发面板自身二进制）。
	//
	// 这里用 selfPath() 而不是相对路径 "./monitor"：
	//   - 相对路径取决于进程的当前工作目录。systemd unit 里若没写
	//     WorkingDirectory（或写成了别处），这里会直接 404；
	//   - 自更新替换的是「正在运行的二进制」（selfPath()），两者一旦
	//     不是同一个文件，就会出现「面板已升级、下发的还是旧版」。
	r.GET("/api/download", func(c *gin.Context) {
		raw := c.Query("arch")
		self := selfPath()
		if raw == "" {
			c.File(self)
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
			c.File(self)
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
		if clientToken == "" || clientToken != t {
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
		t, _ := template.New("s").Parse(htmlLogin)
		t.Execute(c.Writer, map[string]interface{}{
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
			hashed, err := hashPwd(p)
			if err != nil {
				return err
			}
			return tx.Create(&User{Username: u, Password: hashed}).Error
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
		t, _ := template.New("l").Parse(htmlLogin)
		t.Execute(c.Writer, map[string]interface{}{
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
					// 升级失败不阻断登录：用户凭据是对的，哈希格式换不换是内部事。
					// 记一条日志，下次登录会再试一次。
					if upgraded, err := hashPwd(p); err == nil {
						db.Model(&User{}).Where("id = ?", user.ID).Update("password", upgraded)
					} else {
						log.Printf("[auth] 密码哈希升级失败（不影响本次登录）: %v", err)
					}
				}
				loginSucceeded(c)
				s := sessions.Default(c)
				s.Set("user", u)
				s.Set("epoch", sessionEpoch())
				s.Save()
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
		s.Save()
		c.Redirect(302, "/")
	})

	api := r.Group("/api")
	{
		api.POST("/report", func(c *gin.Context) {
			globalConfig.RLock()
			t := globalConfig.Token
			targets := globalConfig.PingTargets
			globalConfig.RUnlock()

			// 先限长再读 body。正常心跳报文只有几 KB，256KB 已经非常宽松。
			// 不加这个上限的话，任何持有 Token 的客户端都能发一个几百 MB 的
			// JSON，直接把面板的内存吃光（gin 默认不限制请求体大小）。
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxReportBytes)

			// 用恒定时间比较。原实现的 `!=` 会在第一个不同的字节处提前返回，
			// 理论上可以靠响应耗时逐字节把 Token 试出来。
			clientToken := c.GetHeader("Authorization")
			if clientToken == "" {
				clientToken = c.Query("token") // 兼容旧方式
			}
			if subtle.ConstantTimeCompare([]byte(clientToken), []byte(t)) != 1 {
				c.AbortWithStatus(401)
				return
			}

			var s SystemStatus
			if err := c.ShouldBindJSON(&s); err != nil {
				// 原实现在绑定失败时不写任何响应，gin 会返回 200 + 空 body。
				// 客户端据此认为「上报成功」，实际一条都没落库 ——
				// 这类静默失败在排查节点异常时极难定位。
				c.JSON(400, gin.H{"error": "报文格式不正确"})
				return
			}

			// 先收敛不可信字段，再进入缓存 / 数据库 / 界面
			sanitizeReport(&s)
			if s.AgentID == "" {
				c.JSON(400, gin.H{"error": "缺少 agent_id"})
				return
			}
			s.LastUpdate = time.Now()
			if s.IP == "" {
				// 仅作兜底：正常 Agent 会自己上报 IP。这里同样走 clientIP，
				// 使得「面板挂在反代后」时落库的也是真实来源而非代理地址。
				s.IP = clientIP(c)
			}

			var node Node
			err := db.First(&node, "agent_id = ?", s.AgentID).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				// 自动注册：节点记录可能被误删，允许 Agent 自愈重建。
				// 但必须有个上限 —— 否则任何持有 Token 的人都能无限灌节点记录，
				// 把库撑大、把面板列表撑爆。
				var total int64
				db.Model(&Node{}).Count(&total)
				if total >= maxAutoRegisterNodes {
					c.JSON(429, gin.H{"error": "节点数量已达上限，请先在面板中清理"})
					return
				}
				db.Clauses(clause.OnConflict{DoNothing: true}).Create(&Node{AgentID: s.AgentID})
				if err := db.First(&node, "agent_id = ?", s.AgentID).Error; err != nil {
					c.JSON(500, gin.H{"error": "创建节点记录失败"})
					return
				}
			} else if err != nil {
				c.JSON(500, gin.H{"error": "读取节点记录失败"})
				return
			}

			if node.Denied {
				c.JSON(200, AgentResponse{Status: "stop"})
				return
			}

			if node.CountryCode == "" && s.IP != "" {
				go lookupCountryCode(s.AgentID, s.IP)
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

			// 历史采样一次批量写入。原实现是「每个 ping 目标一条 db.Create」，
			// 每个目标都是一次独立事务 —— 10 个目标 × 1000 节点 = 每 5 秒
			// 上万次事务提交，SQLite 的 WAL 会被这串 fsync 拖垮。
			now := time.Now()
			rows := make([]MonitorHistory, 0, len(s.PingResults)+3)
			for target, delay := range s.PingResults {
				if delay <= 0 || delay > maxPingDelayMs {
					continue
				}
				rows = append(rows, MonitorHistory{
					AgentID: s.AgentID, Type: "ping",
					Target: cleanField(target, 128), Value: float64(delay), CreatedAt: now,
				})
			}
			if now.Second() < 5 {
				rows = append(rows,
					MonitorHistory{AgentID: s.AgentID, Type: "cpu", Value: s.CPUUsage, CreatedAt: now},
					MonitorHistory{AgentID: s.AgentID, Type: "mem", Value: s.MemUsedPercent, CreatedAt: now},
					MonitorHistory{AgentID: s.AgentID, Type: "disk", Value: s.DiskUsedPercent, CreatedAt: now},
				)
			}
			if len(rows) > 0 {
				db.CreateInBatches(&rows, 50)
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

				id, err := randomAgentID()
				if err != nil {
					log.Printf("[node] 生成节点 ID 失败: %v", err)
					c.JSON(500, gin.H{"status": "error"})
					return
				}

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
				t := strings.TrimSpace(c.PostForm("token"))
				// Token 会被拼进 Agent 安装命令（单引号包裹）、写进 systemd unit
				// 与 OpenRC 脚本。允许引号 / 分号 / 换行，等于把这三处都变成
				// 任意命令与配置注入的载体。直接限定字符集最不容易出错。
				if !validToken(t) {
					c.String(400, "Token 需为 8~64 位的字母、数字、下划线或短横线")
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

	srv := &http.Server{
		Addr:    ":" + port,
		Handler: r,
		// 显式设置超时。gin 的 r.Run() 内部走 http.ListenAndServe，
		// 三个超时全是 0（不限制）：一条慢速连接（Slowloris）就能长期
		// 占住一个连接和一个 goroutine，几十条就能把面板拖到不响应。
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		// 写超时给得宽一些：数据库备份（VACUUM INTO）与导出是同步执行的长任务
		WriteTimeout:   180 * time.Second,
		IdleTimeout:    120 * time.Second,
		MaxHeaderBytes: 1 << 16,
	}

	// 优雅退出：收到 SIGINT/SIGTERM 后停止接受新连接，并给在途请求收尾时间。
	// 自更新脚本会 sleep 3 秒再替换二进制，这个窗口足够旧进程释放端口，
	// 避免新旧实例抢监听（表现为更新完成后短暂 502）。
	go func() {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
		<-ch
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()

	fmt.Printf(">> http://localhost:%s\n", port)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("HTTP 服务异常退出: %v", err)
	}
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

// tokenPattern 约束 Agent 通信 Token 的字符集。
//
// 这个值会被拼进三段不同的上下文：Agent 安装命令（shell 单引号）、
// systemd unit（ExecStart 参数）、OpenRC 脚本（command_args）。
// 与其在每个拼接点各写一套转义，不如把字符集收窄到「放哪儿都安全」。
// 自动生成的 Token 是十六进制，天然满足。
var tokenPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{8,64}$`)

func validToken(s string) bool { return tokenPattern.MatchString(s) }

// randomAgentID 生成节点 ID：8 字节随机数的十六进制表示（16 个字符）。
//
// 原实现只有 3 字节（6 个字符，约 1677 万种）。两个后果：
//   - AgentID 是 nodes 表主键，1000 个节点时的生日碰撞概率就已到 3%。
//     一旦撞上，两台机器会被合并成同一条记录，表现为「刚装好的节点顶着
//     别人的数据」，排查时几乎想不到是 ID 重复。
//   - AgentID 同时是 /api/stats 与 /api/history/* 的公开查询键，
//     熵太低等于允许匿名访问者把全部节点枚举出来。
func randomAgentID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
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
	t, _ := template.New("d").Parse(htmlDashboard)
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

	t.Execute(c.Writer, map[string]interface{}{
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
		// 在线判定窗口由后端下发，前端不再自己硬编码一个魔数。
		// 两边各写一个值时（曾经是后端 30s / 前端 25s），出现
		// 「面板显示在线、告警已判离线」这种自相矛盾的状态只是时间问题。
		"OnlineWindow": int(onlineWindow.Seconds()),
	})
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
				s.Save()
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

// bcryptInput 把口令压成 bcrypt 能接受的输入。
//
// bcrypt 的输入上限是 72 字节，超长会直接返回错误。原实现在出错时
// 「静默退回 SHA-256」，于是只要把密码设得足够长（passwordWeakness 允许
// 到 128 字符），拿到的就是一个抗爆破能力差了几个数量级的旧式哈希 ——
// 而且界面上完全看不出来。
//
// 现在改为：超过 72 字节的口令先做一次 SHA-256 再交给 bcrypt，
// 输入长度恒为 64 字节（hex），永远落在上限内，口令本身的熵没有损失。
//
// 为什么这样改不会锁死存量账号：分支判据是「口令的字节长度」而不是
// 「哈希的形态」，同一个口令在设置与校验时必然走同一分支。因此
//   - 旧库里 bcrypt(raw) 的哈希，只要口令 ≤72 字节就仍然匹配；
//   - 旧库里超长口令本来就走 salt$sha256 分支（当时 bcrypt 直接报错），
//     根本不存在「bcrypt(超长明文)」这种历史哈希。
func bcryptInput(password string) []byte {
	if len(password) <= 72 {
		return []byte(password)
	}
	sum := sha256.Sum256([]byte(password))
	return []byte(hex.EncodeToString(sum[:]))
}

// hashPwd 生成口令哈希。返回 error 而不是降级：
// 「密码学子系统异常」时悄悄换成弱哈希，比直接报错危险得多。
func hashPwd(password string) (string, error) {
	h, err := bcrypt.GenerateFromPassword(bcryptInput(password), bcryptCost)
	if err != nil {
		return "", fmt.Errorf("生成密码哈希失败: %w", err)
	}
	return string(h), nil
}

// checkPwd 校验密码
func checkPwd(password, stored string) bool {
	ok, _ := checkPwdUpgrade(password, stored)
	return ok
}

// checkPwdUpgrade 校验密码；第二个返回值表示「该哈希是旧格式，应升级」
func checkPwdUpgrade(password, stored string) (bool, bool) {
	if strings.HasPrefix(stored, "$2a$") || strings.HasPrefix(stored, "$2b$") || strings.HasPrefix(stored, "$2y$") {
		return bcrypt.CompareHashAndPassword([]byte(stored), bcryptInput(password)) == nil, false
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

	// ping 结果是唯一会被「逐条写进数据库」的字段，必须限幅：
	// 不限条数时，一次心跳就能灌进上万个目标，每 5 秒往
	// monitor_histories 里写几万行 —— 磁盘和清理任务都会被打爆。
	// 同时把键名收敛一遍：它会进数据库、进图表图例。
	//
	// 顺序很重要：先清洗再截断。反过来的话，两个超长键被截断到同一
	// 前缀后会合并成一条，实际保留下来的条数就少于上限了。
	if len(s.PingResults) > 0 {
		cleaned := make(map[string]int64, len(s.PingResults))
		for k, v := range s.PingResults {
			cleaned[cleanField(k, 128)] = v
		}
		if len(cleaned) > maxPingTargetsPerReport {
			trimmed := make(map[string]int64, maxPingTargetsPerReport)
			n := 0
			for k, v := range cleaned {
				trimmed[k] = v
				n++
				if n >= maxPingTargetsPerReport {
					break
				}
			}
			cleaned = trimmed
		}
		s.PingResults = cleaned
	}
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
			// 限长读取：面板的响应正常只有几百字节，1MB 已是极宽松的上限。
			// 不限长时，一个被劫持或被伪造的面板地址可以返回一个巨大的
			// body，把 Agent 的内存吃光 —— 而 Agent 通常跑在资源紧张的小机上。
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
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
