package main

// ================= IP 归属地定位 =================
//
// 原来的实现只有十几行：判空 → http.Get → 写库。它能跑通，但在真实部署里
// 会以三种方式集体失效，而且这三种失效会互相放大：
//
//	① 取到的地址不是节点的公网出口。Agent 上报的结构体里根本没有 IP 字段，
//	   面板只能取 TCP 对端；面板一旦挂在 Nginx / CDN 后面，拿到的是反代地址，
//	   于是所有节点被定位成同一个国家；跑在 Docker NAT 后面则拿到 172.17.0.1，
//	   属于内网地址，永远查不出结果。
//
//	② 查失败不留任何痕迹。country_code 仍为空 → 下一次心跳又查一遍。心跳 5 秒
//	   一次，4 个未定位节点就能打满 ip-api 免费版 45 次/分钟的限额；打满之后
//	   连本来能成功的节点也一起失败，持续超限还会被按 IP 封禁 1 小时。
//
//	③ 只判空、写后不纠。首次查到什么就永久是什么，换 IP、迁移机房都不会更新。
//
// 这一层把 ②③ 一次性解决：结果按 IP 缓存、失败按指数退避、全局令牌闸门限速、
// 显式跳过永远查不出结果的内网地址。① 由「可信代理」配置解决 —— 定位用的地址
// 与登录限流用的地址属于两套相反的信任模型，必须分开，不能为了定位去削弱限流。

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	geoMinBackoff = 3 * time.Minute // 首次失败后的重试间隔
	geoMaxBackoff = 6 * time.Hour   // 退避上限
	geoRatePerMin = 40              // 全局限速，留余量低于 ip-api 免费版的 45/min
	geoHTTPTime   = 6 * time.Second // 单次查询超时
	geoCacheMax   = 5000            // 缓存条数上限，防止被伪造来源撑爆内存
)

// geoClient 带超时。原来的 http.Get 用的是默认客户端，没有超时，
// 上游卡住时 goroutine 会一直挂着不释放。
var geoClient = &http.Client{Timeout: geoHTTPTime}

type geoEntry struct {
	code    string    // 两位国家码；空表示尚未成功定位
	forIP   string    // 该结果对应的来源 IP —— IP 一变即作废
	fails   int       // 连续失败次数，用于指数退避
	lastTry time.Time // 上次尝试时刻（成功与失败都记）
}

var geoStore = struct {
	sync.Mutex
	m map[string]*geoEntry
}{m: make(map[string]*geoEntry)}

func geoCacheSize() int {
	geoStore.Lock()
	defer geoStore.Unlock()
	return len(geoStore.m)
}

// geoGate 全局令牌闸门：把任意 60 秒窗口内的请求数压在 geoRatePerMin 以内。
//
// 用「下次可请求时刻」而不是 time.Ticker —— ticker 在无人消费时会堆积令牌，
// 突发流量一次性打出去照样会撞上限流。
var geoGate struct {
	sync.Mutex
	next time.Time
}

func geoAcquire() bool {
	geoGate.Lock()
	defer geoGate.Unlock()
	now := time.Now()
	if now.Before(geoGate.next) {
		return false
	}
	geoGate.next = now.Add(time.Minute / geoRatePerMin)
	return true
}

// geoUsableIP 判断这个地址值不值得去查。
//
// 内网 / 回环 / 链路本地地址在 ip-api 只会返回 "private range"，永远不可能成功。
// 原实现会把它们当成普通失败，每 5 秒重试一次 —— 这是打满配额最主要的来源，
// 在 Docker 部署下尤其明显（网关地址恒为 172.17.0.1）。
func geoUsableIP(ip string) bool {
	p := net.ParseIP(strings.TrimSpace(ip))
	if p == nil {
		return false
	}
	return !(p.IsPrivate() || p.IsLoopback() || p.IsLinkLocalUnicast() ||
		p.IsLinkLocalMulticast() || p.IsUnspecified() || p.IsMulticast())
}

// geoCachedCountry 只读缓存，不产生任何网络请求。
// 心跳主流程用它把已知结果同步填上，保证界面立刻就是对的。
func geoCachedCountry(ip, fallback string) string {
	if !geoUsableIP(ip) {
		return fallback
	}
	geoStore.Lock()
	defer geoStore.Unlock()
	if e := geoStore.m[ip]; e != nil && e.code != "" && e.forIP == ip {
		return e.code
	}
	return fallback
}

// lookupCountry 取 IP 的国家码，必要时发起一次查询。
//
// 返回 ("", false) 表示「本轮不该重试」（退避未到 / 限速闸门未放行 / 地址不可用），
// 调用方直接跳过即可，不要把它当成查询失败去写日志。
func lookupCountry(ip string) (string, bool) {
	if !geoUsableIP(ip) {
		return "", false
	}
	now := time.Now()

	geoStore.Lock()
	e := geoStore.m[ip]
	if e == nil {
		if len(geoStore.m) >= geoCacheMax {
			pruneGeoLocked(now)
			if len(geoStore.m) >= geoCacheMax {
				geoStore.Unlock()
				return "", false // 缓存已满，本轮放弃而不是无限增长
			}
		}
		e = &geoEntry{}
		geoStore.m[ip] = e
	}
	// 命中：这个结果就是当前 IP 查出来的，直接给
	if e.code != "" && e.forIP == ip {
		geoStore.Unlock()
		return e.code, true
	}
	// 退避窗口内不重试
	if !e.lastTry.IsZero() {
		backoff := geoMinBackoff
		if e.fails > 0 {
			backoff = geoMinBackoff << uint(e.fails-1)
		}
		if backoff <= 0 || backoff > geoMaxBackoff {
			backoff = geoMaxBackoff
		}
		if now.Sub(e.lastTry) < backoff {
			geoStore.Unlock()
			return "", false
		}
	}
	// 先记下本次尝试时刻：同一 IP 的并发心跳不会重复把请求打出去
	e.lastTry = now
	geoStore.Unlock()

	// 闸门在锁外获取 —— 拿不到就本轮放弃，等下一次心跳
	if !geoAcquire() {
		return "", false
	}

	code, ok := queryIPAPI(ip)

	geoStore.Lock()
	if ok {
		e.code, e.forIP, e.fails = code, ip, 0
	} else {
		e.fails++
	}
	geoStore.Unlock()

	if !ok {
		return "", false
	}
	return code, true
}

// pruneGeoLocked 清理长期失败且已超过最大退避窗口的条目。
// 已成功定位的条目一律保留 —— 它们对应稳定的 IP → 国家映射，重新查是浪费配额。
func pruneGeoLocked(now time.Time) {
	for k, e := range geoStore.m {
		if e.code == "" && now.Sub(e.lastTry) > geoMaxBackoff {
			delete(geoStore.m, k)
		}
	}
}

// queryIPAPI 真正发一次请求。
//
// 免费版只提供 HTTP（HTTPS 属付费能力），所以这里没法用 https —— 也正因如此，
// 返回值只能当作展示用的参考，不能用于任何安全判断。
func queryIPAPI(ip string) (string, bool) {
	req, err := http.NewRequest("GET",
		"http://ip-api.com/json/"+ip+"?fields=status,message,countryCode", nil)
	if err != nil {
		return "", false
	}
	req.Header.Set("User-Agent", "HubMonitor-Geo")

	resp, err := geoClient.Do(req)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()

	// 官方文档明确要求：X-Rl 为 0 时必须停止请求，等 X-Ttl 秒之后再继续。
	// 不处理这个头的话，撞上 429 甚至被整段封禁都无从察觉。
	if resp.Header.Get("X-Rl") == "0" {
		if ttl, err := strconv.Atoi(resp.Header.Get("X-Ttl")); err == nil && ttl > 0 {
			geoGate.Lock()
			geoGate.next = time.Now().Add(time.Duration(ttl) * time.Second)
			geoGate.Unlock()
		}
	}
	if resp.StatusCode != 200 {
		return "", false
	}

	var res struct {
		Status      string `json:"status"`
		CountryCode string `json:"countryCode"`
	}
	// 限长读取：这个响应理论上不该很大，多一道保险
	if json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&res) != nil {
		return "", false
	}
	if res.Status != "success" || len(res.CountryCode) != 2 {
		return "", false
	}
	return strings.ToUpper(res.CountryCode), true
}

// syncNodeCountry 把定位结果落到节点表。
//
// 与旧实现的关键差别：不再用「country_code 为空」作为唯一条件，而是每轮都按
// 当前来源 IP 取一次结果 —— 来源 IP 变了，缓存条目自然失效，于是迁移机房、
// 更换 IP 的情况也能自动纠正。
func syncNodeCountry(agentID, ip, currentCode, currentIP string) {
	// geo_ip 为 manual 表示管理员手动指定过，不再让自动定位覆盖
	if currentIP == "manual" {
		return
	}
	code, ok := lookupCountry(ip)
	if !ok {
		return
	}
	if db == nil || (code == currentCode && ip == currentIP) {
		return
	}
	// 同时写入 geo_ip：下一次心跳靠它判断「这个国家码是不是当前 IP 查出来的」
	if err := db.Model(&Node{}).Where("agent_id = ?", agentID).
		Updates(map[string]interface{}{"country_code": code, "geo_ip": ip}).Error; err != nil {
		// 旧实现完全忽略了这里的错误。SQLite 在并发写入时 UPDATE 是可能失败的，
		// 失败无声就意味着 country_code 永远为空，而日志里什么都看不到。
		log.Printf("[geo] 写入节点 %s 归属地失败: %v", agentID, err)
	}
}

// resetGeo 清掉节点的定位缓存，强制下一轮重新查询。id 为空表示全部。
func resetGeo(id string) int {
	tx := db.Model(&Node{})
	if id != "" {
		tx = tx.Where("agent_id = ?", id)
	}
	var nodes []Node
	tx.Find(&nodes)
	if len(nodes) == 0 {
		return 0
	}

	geoStore.Lock()
	for _, node := range nodes {
		if node.GeoIP != "" && node.GeoIP != "manual" {
			delete(geoStore.m, node.GeoIP)
		}
	}
	geoStore.Unlock()

	// 只清 geo_ip、不清 country_code：查询万一失败，旧旗帜还在，
	// 总比先变成「未定位」、再干等下一轮查询要好。
	tx2 := db.Model(&Node{})
	if id != "" {
		tx2 = tx2.Where("agent_id = ?", id)
	}
	tx2.Where("geo_ip <> ?", "manual").Update("geo_ip", "")
	return len(nodes)
}

func isAlpha2(s string) bool {
	if len([]rune(s)) != 2 {
		return false
	}
	for _, r := range s {
		if (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') {
			return false
		}
	}
	return true
}

// ================= 可信反向代理 =================
//
// 定位需要的是「节点的真实公网出口」，登录限流需要的是「不可伪造的来源」——
// 两者信任模型相反，必须分开处理：
//
//	· 登录限流永远用 RemoteIP（TCP 对端），请求方改任何头都绕不过；
//	· 定位允许在管理员显式配置了可信代理网段之后，才采信 X-Forwarded-For。
//
// 之所以要显式配置而不是无条件信任 XFF：XFF 是请求方随手就能编的。
// 一旦无条件采信，等于把「这个节点属于哪个国家」交给对方决定。

var trustedProxyNets struct {
	sync.RWMutex
	nets []*net.IPNet
}

// reloadTrustedProxies 重新解析配置里的网段。保存配置后需要调一次。
func reloadTrustedProxies() {
	globalConfig.RLock()
	raw := globalConfig.TrustedProxies
	globalConfig.RUnlock()

	var nets []*net.IPNet
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		// 允许写裸 IP（自动按 /32 或 /128 处理），也允许写完整 CIDR
		if !strings.Contains(part, "/") {
			ip := net.ParseIP(part)
			if ip == nil {
				continue
			}
			bits := 32
			if ip.To4() == nil {
				bits = 128
			}
			part = fmt.Sprintf("%s/%d", ip.String(), bits)
		}
		if _, n, err := net.ParseCIDR(part); err == nil {
			nets = append(nets, n)
		}
	}

	trustedProxyNets.Lock()
	trustedProxyNets.nets = nets
	trustedProxyNets.Unlock()
}

// validateProxyCIDRs 校验管理员填写的网段，返回空串表示通过。
func validateProxyCIDRs(raw string) string {
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if strings.Contains(part, "/") {
			if _, _, err := net.ParseCIDR(part); err != nil {
				return "网段格式不正确：" + part + "（应为 10.0.0.0/8 或 172.17.0.0/16 这种形式）"
			}
			continue
		}
		if net.ParseIP(part) == nil {
			return "地址格式不正确：" + part
		}
	}
	return ""
}

func ipInTrustedProxy(ip string) bool {
	p := net.ParseIP(ip)
	if p == nil {
		return false
	}
	trustedProxyNets.RLock()
	defer trustedProxyNets.RUnlock()
	for _, n := range trustedProxyNets.nets {
		if n.Contains(p) {
			return true
		}
	}
	return false
}

// geoSourceIP 解析用于归属地定位的来源地址。
//
// 与 loginKey 刻意分开：登录限流必须用不可伪造的 RemoteIP（否则改个头就能绕过），
// 而定位在反向代理后必须能拿到真实客户端地址。两者信任模型不同，
// 不能为了其中一个去牺牲另一个。
func geoSourceIP(c *gin.Context) string {
	trustedProxyNets.RLock()
	hasTrusted := len(trustedProxyNets.nets) > 0
	trustedProxyNets.RUnlock()

	// 未配置可信代理：面板直接对外，只认 TCP 对端
	if !hasTrusted {
		return c.RemoteIP()
	}

	// 先确认「直连对端本身可信」，否则任意来源都能伪造一整串 XFF
	if !ipInTrustedProxy(c.RemoteIP()) {
		return c.RemoteIP()
	}

	xff := c.GetHeader("X-Forwarded-For")
	if xff == "" {
		return c.RemoteIP()
	}
	parts := strings.Split(xff, ",")
	// 从右往左找第一个不属于可信代理的地址：那才是真实客户端。
	// 右边的是各级代理自己追加的，左边的是客户端可能伪造的。
	for i := len(parts) - 1; i >= 0; i-- {
		ip := strings.TrimSpace(parts[i])
		if ip == "" {
			continue
		}
		if !ipInTrustedProxy(ip) {
			return ip
		}
	}
	return c.RemoteIP()
}
