package main

import (
	"bytes"
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
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"

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
		// === 来源地址：可信反向代理白名单（详见「来源地址与地理位置」）===
		TrustedProxies string
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
	LastGeoIP     string    `gorm:"default:''"`    // [新增] 上次做地理位置解析时用的地址
	GeoAt         time.Time // [新增] 上次解析时间，用于判断是否需要重查
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
`, binPath, server, token, id)
	ioutil.WriteFile("/etc/systemd/system/monitor.service", []byte(serviceContent), 0644)
	exec.Command("systemctl", "daemon-reload").Run()
	exec.Command("systemctl", "enable", "monitor").Run()
	exec.Command("systemctl", "restart", "monitor").Run()
	fmt.Println("✅ 安装成功! 服务已启动并设置开机自启。")
}

func installOpenRC(binPath, server, token, id string) {
	fmt.Println("-> 检测到 Alpine (OpenRC) 系统")
	scriptContent := fmt.Sprintf(`#!/sbin/openrc-run
name="monitor"
command="%s"
command_args="-mode agent -server %s -token %s -id %s"
command_background=true
pidfile="/run/monitor.pid"
`, binPath, server, token, id)
	ioutil.WriteFile("/etc/init.d/monitor", []byte(scriptContent), 0755)
	exec.Command("rc-update", "add", "monitor").Run()
	exec.Command("rc-service", "monitor", "restart").Run()
	fmt.Println("✅ 安装成功! 服务已启动并设置开机自启。")
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

// ================= 来源地址与地理位置 =================
//
// 这一节回答一个看着简单、实际很容易做错的问题：面板该把「哪个地址」
// 当成节点的地址。
//
// 面板其实有两个用途完全不同的 IP 概念，早期版本把它们混成了一个：
//
//	① 审计 / 限流用的来源地址 —— 必须是真实 TCP 对端（RemoteIP）。
//	   转发头是请求方随手就能写的，采信它等于让限流形同虚设（见 admin.go 的 loginKey）。
//	② 展示 / 定位用的客户端地址 —— 在反代或容器里，真实客户端地址只存在于
//	   X-Forwarded-For 这类头部中，必须采信，否则拿到的是代理自己的地址。
//
// 早期实现只有 ①：SetTrustedProxies(nil) 之后 ClientIP() 退化成 RemoteIP()，
// 于是面板前面只要有一层 nginx / CDN，所有节点都会被定位成代理所在国
//（典型现象：满屏同一面国旗）；若是同机反代或 Docker 桥接，对端是
// 127.0.0.1 / 172.17.0.1 这类私网地址，ip-api 直接返回 fail，
// country_code 为空，界面上就是一面白旗。
//
// 修法不是「把转发头全部信任」——那会把 ① 一起废掉——而是给 ② 一条显式的、
// 可配置的可信代理链：只有对端确实落在白名单里，才采信转发头。

// geoSkipNets 这些网段永远不会出现在公网上，拿去定位只会浪费一次请求
// （ip-api 对私网地址会返回 status=fail / reserved range）。
var geoSkipNets = func() []*net.IPNet {
	cidrs := []string{
		"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16",
		"127.0.0.0/8", "169.254.0.0/16",
		"100.64.0.0/10", // CGNAT：运营商级 NAT，公网上不可路由
		"fc00::/7", "fe80::/10",
	}
	out := make([]*net.IPNet, 0, len(cidrs))
	for _, s := range cidrs {
		if _, n, err := net.ParseCIDR(s); err == nil {
			out = append(out, n)
		}
	}
	return out
}()

func isPublicIP(ip string) bool {
	p := net.ParseIP(strings.TrimSpace(ip))
	if p == nil {
		return false
	}
	if p.IsLoopback() || p.IsPrivate() || p.IsUnspecified() ||
		p.IsLinkLocalUnicast() || p.IsLinkLocalMulticast() {
		return false
	}
	for _, n := range geoSkipNets {
		if n.Contains(p) {
			return false
		}
	}
	return true
}

// ================= 可信代理白名单 =================

// 解析后的白名单缓存。用读写锁保护，支持保存后立即生效，
// 不必重启面板 —— 否则用户改完白名单看不到变化，只会以为功能坏了。
var trustedProxyCache struct {
	sync.RWMutex
	nets []*net.IPNet
	ips  []net.IP
}

// splitProxyList 把逗号 / 分号 / 空白分隔的列表切成条目，无法识别的直接丢弃。
// 解析失败不报错：宁可少信任一层，也不能让面板起不来。
func splitProxyList(raw string) []string {
	out := make([]string, 0, 4)
	for _, part := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
	}) {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if net.ParseIP(part) != nil {
			out = append(out, part)
			continue
		}
		if _, _, err := net.ParseCIDR(part); err == nil {
			out = append(out, part)
			continue
		}
		log.Printf("[proxy] 忽略无法识别的可信代理条目: %q", part)
	}
	return out
}

func setTrustedProxies(raw string) {
	var nets []*net.IPNet
	var ips []net.IP
	for _, part := range splitProxyList(raw) {
		if _, n, err := net.ParseCIDR(part); err == nil {
			nets = append(nets, n)
			continue
		}
		if p := net.ParseIP(part); p != nil {
			ips = append(ips, p)
		}
	}
	trustedProxyCache.Lock()
	trustedProxyCache.nets, trustedProxyCache.ips = nets, ips
	trustedProxyCache.Unlock()
	if len(nets)+len(ips) > 0 {
		log.Printf("[proxy] 已信任 %d 条代理来源，将据此采信 X-Forwarded-For", len(nets)+len(ips))
	}
}

func isTrustedProxy(ip string) bool {
	p := net.ParseIP(strings.TrimSpace(ip))
	if p == nil {
		return false
	}
	trustedProxyCache.RLock()
	defer trustedProxyCache.RUnlock()
	for _, n := range trustedProxyCache.nets {
		if n.Contains(p) {
			return true
		}
	}
	for _, t := range trustedProxyCache.ips {
		if t.Equal(p) {
			return true
		}
	}
	return false
}

// normalizeProxyList 校验并规范化用户填写的白名单。
//
// 这里必须挡住 0.0.0.0/0（以及等价的 ::/0）：一旦信任全部来源，
// 任何人只要在请求里加一个 X-Forwarded-For 就能伪造来源地址，
// 登录失败计数会被逐个伪造 IP 绕开 —— 等于把刚补上的限流又拆掉。
func normalizeProxyList(raw string) (string, error) {
	out := make([]string, 0, 4)
	seen := make(map[string]bool, 4)
	for _, part := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
	}) {
		part = strings.TrimSpace(part)
		if part == "" || seen[part] {
			continue
		}
		if _, n, err := net.ParseCIDR(part); err == nil {
			if ones, _ := n.Mask.Size(); ones == 0 {
				return "", fmt.Errorf("不接受 %s：那等于信任任何来源，登录限流会被伪造的转发头绕过", part)
			}
			seen[part] = true
			out = append(out, part)
			continue
		}
		if net.ParseIP(part) != nil {
			seen[part] = true
			out = append(out, part)
			continue
		}
		return "", fmt.Errorf("无法识别的地址或网段：%s", part)
	}
	return strings.Join(out, ","), nil
}

// forwardedClientIP 从转发头里取出真实客户端地址。
//
// 取的是「从右往左第一个不在白名单里的地址」，而不是最左边那一个：
// 最左边那个是请求方自己写进 X-Forwarded-For 的，前面挂多少层代理都改不了
// 这一点 —— 直接采信它等于让任何人都能声明自己是任意 IP。
//
// 注意白名单要尽量写窄。把 10.0.0.0/8 这种整个内网段写进来，会让内网里的
// 客户端地址也被当成「一跳代理」跳过，反而回退到更左侧那个可伪造的值。
func forwardedClientIP(c *gin.Context) string {
	if xff := c.GetHeader("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		for i := len(parts) - 1; i >= 0; i-- {
			ip := strings.TrimSpace(parts[i])
			if net.ParseIP(ip) == nil {
				continue
			}
			if isTrustedProxy(ip) {
				continue // 这一跳本身也是我们的代理，继续往左找
			}
			return canonicalIP(ip)
		}
	}
	if rip := strings.TrimSpace(c.GetHeader("X-Real-IP")); net.ParseIP(rip) != nil {
		return canonicalIP(rip)
	}
	return ""
}

// canonicalIP 统一成规范形式：把 ::ffff:1.2.3.4 这类 IPv4-mapped 地址
// 还原成 1.2.3.4，否则拿去查 ip-api 会直接失败。
func canonicalIP(ip string) string {
	p := net.ParseIP(strings.TrimSpace(ip))
	if p == nil {
		return ""
	}
	if v4 := p.To4(); v4 != nil {
		return v4.String()
	}
	return p.String()
}

// realClientIP 返回用于展示与定位的客户端地址。
//
// 与 c.ClientIP() 的区别：只在【对端确实是白名单里的代理】时才采信转发头，
// 因此伪造请求头无法影响结果；白名单为空时行为与 RemoteIP() 完全一致。
func realClientIP(c *gin.Context) string {
	peer := canonicalIP(c.RemoteIP())
	if peer == "" {
		return ""
	}
	if isTrustedProxy(peer) {
		if ip := forwardedClientIP(c); ip != "" {
			return ip
		}
	}
	return peer
}

// ================= 地理位置解析 =================

const (
	geoFailedRetry  = 6 * time.Hour      // 解析失败（私网 / 被限流）后的重试间隔
	geoRefreshAfter = 7 * 24 * time.Hour // 解析成功后的重查周期
)

// geoEndpoint 定位接口地址。抽成变量有两个用处：
//   - 换服务商时只改这一处（ipwho.is / ipapi.co / 自建 MaxMind 服务都能顶上）；
//   - 便于在测试里指向本地服务 —— 免费接口在部分网络环境下会被直接拦掉
//     （返回 403），没有这个钩子就只能靠人工肉眼验证。
var geoEndpoint = "http://ip-api.com/json/"

// geoInflight 记录正在进行的解析，避免同一节点被并发查询多次。
// 心跳 5 秒一次，而结论要写库之后才会被下一轮看到 —— 没有这个去重，
// 一个新节点会在几秒内白白消耗好几次免费额度（ip-api 免费档 45 次/分钟）。
var geoInflight = struct {
	sync.Mutex
	m map[string]bool
}{m: make(map[string]bool)}

// needGeoLookup 判断这次心跳是否需要（重新）解析地理位置
func needGeoLookup(node Node, ip string) bool {
	if node.LastGeoIP == ip {
		// 同一地址刚查过：失败过的按短周期重试，成功过的按长周期复查
		if node.CountryCode == "" {
			return time.Since(node.GeoAt) > geoFailedRetry
		}
		return time.Since(node.GeoAt) > geoRefreshAfter
	}
	return true // 首次，或节点换了地址（换机房 / 代理配置修正），旧结论不再适用
}

// geoLookupAsync 异步解析节点地址所属国家。
//
// 三个必须守住的点：
//   - ip 为空时绝不能发请求。ip-api 的接口在 ip 为空时会把【请求方自己】
//     （也就是面板服务器）的位置返回回来，于是所有节点都被标成面板所在国 ——
//     这是「旗帜不对」里最难查的一种。
//   - 私网 / 保留地址先挡掉，既省额度也避免拿到无意义的结果。
//   - 必须带超时。默认的 http.Client 没有超时，对方挂住时 goroutine 会一直堆积
//     （alert.go 里为同样的问题专门建了 alertClient，这里早期版本漏了）。
func geoLookupAsync(agentID, ip string) {
	if agentID == "" {
		return
	}
	if !isPublicIP(ip) {
		// 记一笔「已处理」，否则每轮心跳都会重新判断一遍
		stampGeo(agentID, ip, "")
		return
	}

	geoInflight.Lock()
	if geoInflight.m[agentID] {
		geoInflight.Unlock()
		return
	}
	geoInflight.m[agentID] = true
	geoInflight.Unlock()

	go func() {
		defer func() {
			geoInflight.Lock()
			delete(geoInflight.m, agentID)
			geoInflight.Unlock()
		}()

		cli := &http.Client{Timeout: 8 * time.Second}
		// fields 只取需要的两项：响应体更小，也不浪费免费额度
		u := geoEndpoint + url.PathEscape(ip) + "?fields=status,countryCode"
		resp, err := cli.Get(u)
		if err != nil {
			log.Printf("[geo] 解析 %s 失败: %v", ip, err)
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			log.Printf("[geo] 解析 %s 返回状态码 %d", ip, resp.StatusCode)
			return
		}
		var res struct {
			Status      string `json:"status"`
			CountryCode string `json:"countryCode"`
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&res); err != nil {
			return
		}
		// 免费接口用 status 表达成败（fail 时 countryCode 为空）。
		// 只看 countryCode 会漏掉「查询被拒绝」与「查询成功但无国家」的区别，
		// 也就无法决定该用短周期重试还是长周期复查。
		if res.Status != "success" {
			log.Printf("[geo] 解析 %s 被拒绝（status=%s）", ip, res.Status)
			stampGeo(agentID, ip, "")
			return
		}
		cc := strings.ToUpper(strings.TrimSpace(res.CountryCode))
		if len(cc) != 2 {
			stampGeo(agentID, ip, "")
			return
		}
		stampGeo(agentID, ip, cc)
	}()
}

// stampGeo 记录本次解析用的地址、时间与结论，并同步刷新内存缓存。
//
// 存 LastGeoIP 是为了「节点换了机房 / 代理配置修正」时能自动重新定位；
// 存 GeoAt 是为了定期重查 —— 早期实现只在 country_code 为空时查询，
// 一旦写进一个错误的国家就再也纠正不回来。
func stampGeo(agentID, ip, cc string) {
	upd := map[string]interface{}{
		"last_geo_ip": ip,
		"geo_at":      time.Now(),
	}
	if cc != "" {
		upd["country_code"] = cc
	}
	if db != nil {
		if err := db.Model(&Node{}).Where("agent_id = ?", agentID).Updates(upd).Error; err != nil {
			log.Printf("[geo] 写入失败: %v", err)
			return
		}
	}
	if cc == "" {
		return
	}
	// 缓存里也同步一份，省得等下一轮心跳或 /api/stats 才纠正过来
	cacheMutex.Lock()
	if st, ok := statusCache[agentID]; ok {
		st.CountryCode = cc
		statusCache[agentID] = st
	}
	cacheMutex.Unlock()
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

	// 安全增强：不信任任何代理头。
	// gin 默认把 X-Forwarded-For 当作可信来源，于是 ClientIP() 可被请求方随意伪造 ——
	// 对「登录限流」这种按 IP 计数的防护来说，等于形同虚设。
	// 这里显式关掉信任代理，ClientIP()/RemoteIP() 一律回落到真实 TCP 对端。
	//
	// 注意：界面展示与地理位置解析需要的「真实客户端地址」【不走】gin 这套机制，
	// 而是由 realClientIP() 按「可信代理白名单」自行判断（见「来源地址与地理位置」）。
	// 于是限流链路只认 RemoteIP、展示链路才采信转发头，两者互不影响 ——
	// 早期版本两条链路共用 ClientIP()，一旦面板前面挂了反代，
	// 所有节点都会被定位成代理所在国（或因为拿到私网地址而显示白旗）。
	_ = r.SetTrustedProxies(nil)
	globalConfig.RLock()
	tps := globalConfig.TrustedProxies
	globalConfig.RUnlock()
	setTrustedProxies(tps)

	// PWA：manifest / Service Worker / 运行时绘制的图标 / 离线页
	registerPWARoutes(r)

	// 安全增强: 随机生成 Session Key
	var sessionKey []byte
	if envKey := os.Getenv("SESSION_KEY"); envKey != "" {
		sessionKey = []byte(envKey)
	} else {
		sessionKey = make([]byte, 32)
		rand.Read(sessionKey)
	}
	store := cookie.NewStore(sessionKey)
	// 安全增强: Cookie 属性设置
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
					db.Model(&User{}).Where("id = ?", user.ID).
						Update("password", hashPwd(p))
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
			// 安全增强: 优先从 Header 获取 Token
			clientToken := c.GetHeader("Authorization")
			if clientToken == "" {
				clientToken = c.Query("token")
			} // 兼容旧方式

			if clientToken != t {
				c.AbortWithStatus(401)
				return
			}

			var s SystemStatus
			if err := c.ShouldBindJSON(&s); err == nil {
				// 先收敛不可信字段，再进入缓存 / 数据库 / 界面
				sanitizeReport(&s)
				s.LastUpdate = time.Now()
				// 节点地址一律以连接来源为准：上报体里的 IP 字段任何持有 Token 的
				// 客户端都能伪造，而它同时用于界面展示与地理位置解析 ——
				// 采信它等于让别人替你决定卡片上显示哪个国家。
				// realClientIP 只在【对端确实属于可信代理】时才采信转发头。
				if ip := realClientIP(c); ip != "" {
					s.IP = ip
				}

				var node Node
				db.Clauses(clause.OnConflict{DoNothing: true}).Create(&Node{AgentID: s.AgentID})
				db.First(&node, "agent_id = ?", s.AgentID)

				if node.Denied {
					c.JSON(200, AgentResponse{Status: "stop"})
					return
				}

				// 地理位置解析：仅在「没有结论 / 地址变了 / 距上次解析超过重查周期」
				// 时才做，判定见 needGeoLookup。
				// 早期实现只在 country_code 为空时查询，一旦写进一个错误的国家
				// （反代场景下非常容易发生）就再也纠正不回来了。
				if needGeoLookup(node, s.IP) {
					geoLookupAsync(s.AgentID, s.IP)
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

	fmt.Printf(">> http://localhost:%s\n", port)
	r.Run(":" + port)
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
	// 默认为空 = 不信任任何代理，与改动前的行为完全一致（直连部署无需配置）
	globalConfig.TrustedProxies = ""
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
		// === 来源地址 ===
		case "trusted_proxies":
			globalConfig.TrustedProxies = c.Value
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
			body, _ := ioutil.ReadAll(resp.Body)
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
