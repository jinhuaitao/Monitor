package main

// ================= 系统管理 · 运维与安全 =================
//
// 本文件集中承载「系统管理」里偏运维与安全的能力，避免继续往 main.go 里堆：
//
//	① 审计日志   —— 关键操作留痕，出事能回溯
//	② 登录限流   —— 按来源 IP 计数的失败锁定，挡住在线爆破
//	③ 账号安全   —— 改密码 / 改用户名 / 会话版本失效
//	④ 系统信息   —— 面板自身运行指标（内存、协程、数据库体积、节点统计）
//	⑤ 数据管理   —— 历史保留期、统计、清理、导出、在线备份
//	⑥ 节点批量   —— 分组 / 维护模式 / 静音 / 批量删除与更新
//
// 贯穿这些功能的一条原则：凡是能影响到「全部被控服务器」的动作
//（改 Token、改更新源、下发更新），都必须既留痕、又能被一眼看见。

import (
	"encoding/csv"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-contrib/sessions"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/disk"
	"github.com/shirou/gopsutil/v3/host"
	"github.com/shirou/gopsutil/v3/mem"
	"gorm.io/gorm"
)

// panelStart 面板进程启动时刻，用于计算运行时长
var panelStart = time.Now()

// ================= 数据模型 =================

// AuditLog 管理员操作审计。
//
// 为什么必须有这张表：面板的「下发客户端更新」落到节点后，
// 是【以 root 身份替换二进制并重启服务】。等于一条对所有被控机器的
// 执行通道。没有审计日志时，事后无法回答「是谁、什么时候、把更新源
// 换成了哪个仓库」—— 而这三者恰好是追责链条上最关键的三个点。
type AuditLog struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	CreatedAt time.Time `gorm:"index" json:"created_at"`
	Username  string    `gorm:"index" json:"username"`
	IP        string    `json:"ip"`
	Action    string    `gorm:"index" json:"action"`
	Target    string    `json:"target"`
	Detail    string    `json:"detail"`
	Success   bool      `json:"success"`
}

// AlertEvent 告警事件历史。用于回答「昨晚到底报过什么」——
// 只发通知不落库的话，值班的人翻聊天记录才知道发生过什么。
type AlertEvent struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	CreatedAt time.Time `gorm:"index" json:"created_at"`
	AgentID   string    `gorm:"index" json:"agent_id"`
	NodeName  string    `json:"node_name"`
	Kind      string    `gorm:"index" json:"kind"` // offline / online / cpu / mem / disk
	Level     string    `json:"level"`             // info / warning / critical
	Message   string    `json:"message"`
}

// ================= 会话与身份 =================

// sessionEpoch 当前会话版本。改密码 / 改用户名后前进一格，
// 于是所有设备上的旧 Cookie 立刻失效 —— Cookie 存储是无状态的，
// 没有这个版本号就只能等 Cookie 自己过期。
func sessionEpoch() string {
	globalConfig.RLock()
	defer globalConfig.RUnlock()
	return globalConfig.SessionEpoch
}

func bumpSessionEpoch() {
	ep := randomEpoch()
	saveConfig("session_epoch", ep)
	globalConfig.Lock()
	globalConfig.SessionEpoch = ep
	globalConfig.Unlock()
}

// currentUser 返回当前登录用户名；未登录或会话已失效时返回空串。
// 全站判断管理员身份都走这里，避免出现「某些接口只查 user 是否存在」
// 这种漏网之鱼 —— 改密码后旧会话仍然能进管理接口就麻烦了。
func currentUser(c *gin.Context) string {
	s := sessions.Default(c)
	raw := s.Get("user")
	if raw == nil {
		return ""
	}
	u, ok := raw.(string)
	if !ok || u == "" {
		return ""
	}
	ep, ok := s.Get("epoch").(string)
	if !ok || ep == "" || ep != sessionEpoch() {
		return ""
	}
	return u
}

func isAdminSession(c *gin.Context) bool { return currentUser(c) != "" }

// ================= 审计 =================

// auditAs 记录一条操作审计。
//
// 审计是旁路：写失败只打日志，绝不能把业务接口一起带崩 ——
// 「因为审计表锁了所以改不了配置」比没有审计更糟。
func auditAs(c *gin.Context, username, action, target, detail string, ok bool) {
	entry := AuditLog{
		CreatedAt: time.Now(),
		Action:    cleanField(action, 32),
		Target:    cleanField(target, 160),
		Detail:    cleanField(detail, 400),
		Success:   ok,
	}
	if c != nil {
		if username == "" {
			username = currentUser(c)
		}
		// 用 RemoteIP 而不是 ClientIP：审计里的来源地址不该被请求头改写
		entry.IP = cleanField(c.RemoteIP(), 64)
	}
	entry.Username = cleanField(username, 64)

	if db == nil {
		return
	}
	if err := db.Create(&entry).Error; err != nil {
		log.Printf("[audit] 写入失败: %v", err)
	}
}

func audit(c *gin.Context, action, target, detail string, ok bool) {
	auditAs(c, "", action, target, detail, ok)
}

// ================= 登录限流 =================
//
// 这里刻意【只按来源 IP 硬锁定，不按用户名锁定】。
//
// 按用户名锁定看起来更"精准"，实际上会被当成 DoS 工具：
// 攻击者只要拿错误密码狂刷 admin，真正的管理员就被挡在门外。
// 按 IP 锁定则相反 —— 被挡住的正是发起攻击的那台机器。
//
// 另外两个容易写错、写错就形同虚设的点：
//   - 来源地址必须取 RemoteIP（真实 TCP 对端）。ClientIP 会采信
//     X-Forwarded-For，而那是请求方随手就能编的，等于没有限流。
//   - 已经处于锁定中的请求【不再累加计数】。否则攻击者持续请求
//     就能无限延长锁定窗口，被锁的账号永远解不开。

const (
	loginMaxFails  = 5                // 连续失败次数上限
	loginLockFor   = 10 * time.Minute // 触发上限后的锁定时长
	loginFailSlack = 15 * time.Minute // 失败计数的有效期
	loginGuardCap  = 20000            // 记录条数上限，防止被大量伪造来源撑爆内存
)

type loginAttempt struct {
	fails    int
	firstAt  time.Time
	lockedAt time.Time
}

var loginGuard = struct {
	sync.Mutex
	m map[string]*loginAttempt
}{m: make(map[string]*loginAttempt)}

func loginKey(c *gin.Context) string {
	return cleanField(c.RemoteIP(), 64)
}

func loginGuardSize() int {
	loginGuard.Lock()
	defer loginGuard.Unlock()
	return len(loginGuard.m)
}

// loginBlocked 判断来源是否处于锁定中，返回剩余秒数
func loginBlocked(c *gin.Context) (int, bool) {
	key := loginKey(c)
	if key == "" {
		return 0, false
	}
	now := time.Now()
	loginGuard.Lock()
	defer loginGuard.Unlock()

	e := loginGuard.m[key]
	if e == nil || e.lockedAt.IsZero() {
		return 0, false
	}
	left := loginLockFor - now.Sub(e.lockedAt)
	if left <= 0 {
		// 锁定到期，整条记录作废（而不是留着计数继续沿用）
		delete(loginGuard.m, key)
		return 0, false
	}
	return int(left.Seconds()) + 1, true
}

// loginFailed 记一次失败，返回剩余可尝试次数（0 表示本次已触发锁定）
func loginFailed(c *gin.Context, username string) int {
	key := loginKey(c)
	if key == "" {
		return loginMaxFails
	}
	now := time.Now()
	loginGuard.Lock()
	defer loginGuard.Unlock()

	if len(loginGuard.m) >= loginGuardCap {
		pruneLoginGuardLocked(now)
	}

	e := loginGuard.m[key]
	if e == nil || now.Sub(e.firstAt) > loginFailSlack {
		e = &loginAttempt{firstAt: now}
		loginGuard.m[key] = e
	}
	e.fails++
	if e.fails >= loginMaxFails {
		e.lockedAt = now
		return 0
	}
	return loginMaxFails - e.fails
}

// loginSucceeded 登录成功即清空该来源的失败记录
func loginSucceeded(c *gin.Context) {
	key := loginKey(c)
	if key == "" {
		return
	}
	loginGuard.Lock()
	delete(loginGuard.m, key)
	loginGuard.Unlock()
}

func pruneLoginGuardLocked(now time.Time) {
	for k, e := range loginGuard.m {
		if !e.lockedAt.IsZero() {
			if now.Sub(e.lockedAt) > loginLockFor {
				delete(loginGuard.m, k)
			}
			continue
		}
		if now.Sub(e.firstAt) > loginFailSlack {
			delete(loginGuard.m, k)
		}
	}
}

func pruneLoginGuard() {
	now := time.Now()
	loginGuard.Lock()
	pruneLoginGuardLocked(now)
	loginGuard.Unlock()
}

// ================= 密码强度 =================

// 一个极小的常见弱口令黑名单。不求覆盖全，只挡住「随手能想到的那几个」。
var weakPasswords = map[string]bool{
	"12345678": true, "123456789": true, "1234567890": true, "password": true,
	"password1": true, "passw0rd": true, "qwertyui": true, "qwerty123": true,
	"abc12345": true, "admin123": true, "admin888": true, "root1234": true,
	"11111111": true, "00000000": true, "88888888": true, "iloveyou": true,
	"letmein1": true, "welcome1": true, "monitor123": true, "a1234567": true,
}

// passwordWeakness 返回不通过的原因；空字符串表示通过
func passwordWeakness(username, pwd string) string {
	n := len([]rune(pwd))
	if n < 8 {
		return "密码至少需要 8 位字符"
	}
	if n > 128 {
		return "密码过长，最多 128 位"
	}
	if username != "" && strings.EqualFold(strings.TrimSpace(pwd), strings.TrimSpace(username)) {
		return "密码不能与用户名相同"
	}
	var lower, upper, digit, symbol int
	for _, r := range pwd {
		switch {
		case r >= 'a' && r <= 'z':
			lower++
		case r >= 'A' && r <= 'Z':
			upper++
		case r >= '0' && r <= '9':
			digit++
		default:
			symbol++
		}
	}
	kinds := 0
	for _, k := range []int{lower, upper, digit, symbol} {
		if k > 0 {
			kinds++
		}
	}
	if kinds < 2 {
		return "密码需包含大写字母、小写字母、数字、符号中的至少两类"
	}
	if weakPasswords[strings.ToLower(pwd)] {
		return "该密码过于常见，请更换"
	}
	return ""
}

// ================= 面板自身指标 =================

func databasePath() string {
	if p, err := filepath.Abs(dbPath); err == nil {
		return p
	}
	return dbPath
}

// databaseSize 统计主库 + WAL + SHM 的总体积。
// 只看主文件会明显偏小：WAL 模式下新写入的数据先落在 -wal 里。
func databaseSize() int64 {
	base := databasePath()
	var total int64
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if st, err := os.Stat(base + suffix); err == nil {
			total += st.Size()
		}
	}
	return total
}

func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	units := []string{"KB", "MB", "GB", "TB"}
	v := float64(n)
	i := -1
	for v >= unit && i < len(units)-1 {
		v /= unit
		i++
	}
	return fmt.Sprintf("%.1f %s", v, units[i])
}

// ================= 主机硬件规格 =================
//
// 概览页要展示面板所在机器的 CPU 型号 / 内存大小 / 硬盘大小。
// 这类信息有两个特点，决定了下面的实现方式：
//
//	① 静态 —— CPU 型号、内存总容量、磁盘总容量在进程生命周期内不会变，
//	   而「刷新」按钮会反复打这个接口，所以用 sync.Once 只采集一次；
//	② 易失败 —— 容器里可能读不到 /proc/cpuinfo，非 root 可能 statfs 失败。
//	   任何一个子项失败都只让该字段留空（前端显示「—」），
//	   绝不能让整个接口 500 —— 系统信息页看不到东西，比少显示一项糟糕得多。

type hostSpecStatic struct {
	CPUModel  string
	CPUCores  int
	CPUMhz    float64
	MemTotal  uint64
	DiskTotal uint64
	DiskPath  string
	Hostname  string
	Kernel    string
	OSName    string
}

var (
	hostSpecOnce sync.Once
	hostSpecVal  hostSpecStatic
)

func hostStaticInfo() hostSpecStatic {
	hostSpecOnce.Do(func() {
		var h hostSpecStatic

		if infos, err := cpu.Info(); err == nil && len(infos) > 0 {
			h.CPUModel = strings.TrimSpace(infos[0].ModelName)
			h.CPUMhz = infos[0].Mhz
		}
		// 逻辑核数（含超线程），比 cpu.Info() 里的物理 Cores 更贴近"能跑多少活"
		if n, err := cpu.Counts(true); err == nil && n > 0 {
			h.CPUCores = n
		}
		if vm, err := mem.VirtualMemory(); err == nil {
			h.MemTotal = vm.Total
		}
		if path, total, ok := primaryDisk(); ok {
			h.DiskPath, h.DiskTotal = path, total
		}
		if hi, err := host.Info(); err == nil {
			h.Hostname = hi.Hostname
			h.Kernel = hi.KernelVersion
			h.OSName = strings.TrimSpace(hi.Platform + " " + hi.PlatformVersion)
		}
		hostSpecVal = h
	})
	return hostSpecVal
}

// primaryDisk 找出"根分区"的挂载点与总容量。
//
// 不能写死 disk.Usage("/")：Windows 上 "/" 必然失败。按平台依次尝试，
// 全都失败再退化成"取第一个容量大于 0 的真实分区"——总比显示「—」强。
func primaryDisk() (string, uint64, bool) {
	candidates := []string{"/"}
	if runtime.GOOS == "windows" {
		if sysDrive := os.Getenv("SystemDrive"); sysDrive != "" {
			candidates = []string{sysDrive + `\`, sysDrive}
		}
	}
	for _, p := range candidates {
		if du, err := disk.Usage(p); err == nil && du.Total > 0 {
			return p, du.Total, true
		}
	}
	if parts, err := disk.Partitions(false); err == nil {
		for _, pt := range parts {
			if du, err := disk.Usage(pt.Mountpoint); err == nil && du.Total > 0 {
				return pt.Mountpoint, du.Total, true
			}
		}
	}
	return "", 0, false
}

// hostUsage 读取"当前"的内存与磁盘占用。这两项每次请求都要重新读。
// 部分失败是安全的：失败的那项保持 0，前端按"未知"处理。
func hostUsage(diskPath string) (memTotal, memUsed, diskTotal, diskUsed uint64) {
	if vm, err := mem.VirtualMemory(); err == nil {
		memTotal, memUsed = vm.Total, vm.Used
	}
	if diskPath != "" {
		if du, err := disk.Usage(diskPath); err == nil {
			diskTotal, diskUsed = du.Total, du.Used
		}
	}
	return
}

// ================= 路由注册 =================

func registerAdminRoutes(auth *gin.RouterGroup) {

	// ---------- 系统信息 ----------
	auth.GET("/settings/system/info", func(c *gin.Context) {
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)

		var totalNodes, deniedNodes, pendingNodes, userRows int64
		db.Model(&Node{}).Count(&totalNodes)
		db.Model(&Node{}).Where("denied = ?", true).Count(&deniedNodes)
		db.Model(&Node{}).Where("denied = ? AND pending_update <> ''", false).Count(&pendingNodes)
		db.Model(&User{}).Count(&userRows)

		var onlineNodes int64
		cacheMutex.RLock()
		for _, s := range statusCache {
			if time.Since(s.LastUpdate) <= onlineWindow {
				onlineNodes++
			}
		}
		cacheMutex.RUnlock()

		var historyRows, auditRows, alertRows int64
		db.Model(&MonitorHistory{}).Count(&historyRows)
		db.Model(&AuditLog{}).Count(&auditRows)
		db.Model(&AlertEvent{}).Count(&alertRows)

		globalConfig.RLock()
		keepHours := globalConfig.HistoryKeepHours
		auditDays := globalConfig.AuditKeepDays
		alertDays := globalConfig.AlertKeepDays
		globalConfig.RUnlock()

		// 面板所在主机的硬件规格。静态部分走 sync.Once 缓存，
		// 只有内存/磁盘的"已用量"每次重新读。
		hs := hostStaticInfo()
		hMemTotal, hMemUsed, hDiskTotal, hDiskUsed := hostUsage(hs.DiskPath)
		if hMemTotal == 0 {
			hMemTotal = hs.MemTotal
		}
		if hDiskTotal == 0 {
			hDiskTotal = hs.DiskTotal
		}

		c.JSON(200, gin.H{
			"hostname":        hs.Hostname,
			"kernel":          hs.Kernel,
			"os_name":         hs.OSName,
			"cpu_model":       hs.CPUModel,
			"cpu_cores":       hs.CPUCores,
			"cpu_mhz":         hs.CPUMhz,
			"mem_total":       hMemTotal,
			"mem_used":        hMemUsed,
			"disk_total":      hDiskTotal,
			"disk_used":       hDiskUsed,
			"disk_path":       hs.DiskPath,
			"version":         displayVersion(),
			"commit":          BuildCommit,
			"build_time":      BuildTime,
			"go_version":      runtime.Version(),
			"platform":        runtime.GOOS + "/" + runtime.GOARCH,
			"started_at":      panelStart.Format(time.RFC3339),
			"uptime_sec":      int64(time.Since(panelStart).Seconds()),
			"mem_alloc":       ms.Alloc,
			"mem_sys":         ms.Sys,
			"num_gc":          ms.NumGC,
			"goroutines":      runtime.NumGoroutine(),
			"db_path":         databasePath(),
			"db_size":         databaseSize(),
			"history_rows":    historyRows,
			"audit_rows":      auditRows,
			"alert_rows":      alertRows,
			"node_total":      totalNodes - deniedNodes,
			"node_online":     onlineNodes,
			"node_denied":     deniedNodes,
			"node_pending":    pendingNodes,
			"admin_count":     userRows,
			"online_window":   int(onlineWindow.Seconds()),
			"login_guard":     loginGuardSize(),
			"history_keep":    keepHours,
			"audit_keep_days": auditDays,
			"alert_keep_days": alertDays,
		})
	})

	// ---------- 账号安全 ----------
	auth.POST("/settings/account/password", func(c *gin.Context) {
		user := currentUser(c)
		oldPwd := c.PostForm("old")
		newPwd := c.PostForm("new")
		confirm := c.PostForm("confirm")

		var u User
		if err := db.Where("username = ?", user).First(&u).Error; err != nil {
			c.String(400, "当前账号不存在")
			return
		}
		if !checkPwd(oldPwd, u.Password) {
			audit(c, "password_change", user, "原密码校验失败", false)
			c.String(400, "原密码不正确")
			return
		}
		if newPwd != confirm {
			c.String(400, "两次输入的新密码不一致")
			return
		}
		if checkPwd(newPwd, u.Password) {
			c.String(400, "新密码不能与原密码相同")
			return
		}
		if msg := passwordWeakness(user, newPwd); msg != "" {
			c.String(400, msg)
			return
		}
		if err := db.Model(&User{}).Where("id = ?", u.ID).
			Update("password", hashPwd(newPwd)).Error; err != nil {
			c.String(500, "保存失败，请重试")
			return
		}

		// 会话版本前进一格：其它设备上已经拿到的 Cookie 立刻作废
		bumpSessionEpoch()
		audit(c, "password_change", user, "修改登录密码，全部会话已失效", true)

		// 当前会话同样失效，前端收到 200 后会跳回登录页
		s := sessions.Default(c)
		s.Clear()
		s.Save()
		c.Status(200)
	})

	auth.POST("/settings/account/username", func(c *gin.Context) {
		cur := currentUser(c)
		newName := cleanField(c.PostForm("username"), 32)
		pwd := c.PostForm("password")

		if len([]rune(newName)) < 3 {
			c.String(400, "用户名至少需要 3 个字符")
			return
		}
		var u User
		if err := db.Where("username = ?", cur).First(&u).Error; err != nil {
			c.String(400, "当前账号不存在")
			return
		}
		// 改用户名属于账号凭证变更，必须二次验证
		if !checkPwd(pwd, u.Password) {
			audit(c, "username_change", cur, "验证密码失败", false)
			c.String(400, "请输入当前密码以确认身份")
			return
		}
		var dup int64
		db.Model(&User{}).Where("username = ? AND id <> ?", newName, u.ID).Count(&dup)
		if dup > 0 {
			c.String(400, "该用户名已被占用")
			return
		}
		if err := db.Model(&User{}).Where("id = ?", u.ID).
			Update("username", newName).Error; err != nil {
			c.String(500, "保存失败，请重试")
			return
		}
		s := sessions.Default(c)
		s.Set("user", newName)
		s.Set("epoch", sessionEpoch())
		s.Save()
		auditAs(c, newName, "username_change", cur, "用户名 "+cur+" 变更为 "+newName, true)
		c.Status(200)
	})

	// 登录安全状态：让管理员能看到「谁正在被限流」，而不是只能干等
	auth.GET("/settings/account/security", func(c *gin.Context) {
		loginGuard.Lock()
		type item struct {
			IP      string `json:"ip"`
			Fails   int    `json:"fails"`
			Locked  bool   `json:"locked"`
			LeftSec int    `json:"left_sec"`
			FirstAt string `json:"first_at"`
		}
		now := time.Now()
		items := make([]item, 0, len(loginGuard.m))
		for ip, e := range loginGuard.m {
			it := item{IP: ip, Fails: e.fails, FirstAt: e.firstAt.Format("2006-01-02 15:04:05")}
			if !e.lockedAt.IsZero() {
				if left := loginLockFor - now.Sub(e.lockedAt); left > 0 {
					it.Locked = true
					it.LeftSec = int(left.Seconds()) + 1
				}
			}
			items = append(items, it)
		}
		loginGuard.Unlock()

		globalConfig.RLock()
		epoch := globalConfig.SessionEpoch
		globalConfig.RUnlock()

		c.JSON(200, gin.H{
			"max_fails":      loginMaxFails,
			"lock_minutes":   int(loginLockFor.Minutes()),
			"window_minutes": int(loginFailSlack.Minutes()),
			"entries":        items,
			"session_epoch":  epoch,
			"bcrypt_cost":    bcryptCost,
		})
	})

	// 手动解除某个来源的锁定（万一自己把自己锁了）
	auth.POST("/settings/account/unlock", func(c *gin.Context) {
		ip := cleanField(c.PostForm("ip"), 64)
		if ip == "" {
			loginGuard.Lock()
			loginGuard.m = make(map[string]*loginAttempt)
			loginGuard.Unlock()
			audit(c, "unlock_all", "", "清空全部登录失败计数", true)
			c.Status(200)
			return
		}
		loginGuard.Lock()
		delete(loginGuard.m, ip)
		loginGuard.Unlock()
		audit(c, "unlock", ip, "解除来源 "+ip+" 的登录锁定", true)
		c.Status(200)
	})

	// ---------- 操作日志 ----------
	auth.GET("/settings/audit", func(c *gin.Context) {
		page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
		size, _ := strconv.Atoi(c.DefaultQuery("size", "30"))
		if page < 1 {
			page = 1
		}
		if size < 1 || size > 200 {
			size = 30
		}
		q := cleanField(c.Query("q"), 64)
		action := cleanField(c.Query("action"), 32)

		// 查询条件构造两次而不是复用同一个 *gorm.DB：
		// Count 之后继续 Find 会把 count 的语句状态带过去
		build := func() *gorm.DB {
			tx := db.Model(&AuditLog{})
			if q != "" {
				like := "%" + q + "%"
				tx = tx.Where("username LIKE ? OR target LIKE ? OR detail LIKE ? OR ip LIKE ?",
					like, like, like, like)
			}
			if action != "" && action != "all" {
				tx = tx.Where("action = ?", action)
			}
			return tx
		}

		var total int64
		build().Count(&total)

		rows := make([]AuditLog, 0, size)
		build().Order("created_at DESC").Offset((page - 1) * size).Limit(size).Find(&rows)

		var actions []string
		db.Model(&AuditLog{}).Distinct().Order("action").Pluck("action", &actions)

		c.JSON(200, gin.H{
			"total":   total,
			"page":    page,
			"size":    size,
			"items":   rows,
			"actions": actions,
		})
	})

	auth.POST("/settings/audit/clear", func(c *gin.Context) {
		all := c.PostForm("all") == "1"
		var res *gorm.DB
		if all {
			res = db.Where("1 = 1").Delete(&AuditLog{})
		} else {
			days, _ := strconv.Atoi(c.PostForm("days"))
			if days <= 0 {
				days = 90
			}
			res = db.Where("created_at < ?", time.Now().AddDate(0, 0, -days)).Delete(&AuditLog{})
		}
		if res.Error != nil {
			c.String(500, "清理失败："+res.Error.Error())
			return
		}
		audit(c, "audit_clear", "", fmt.Sprintf("清理操作日志，删除 %d 条", res.RowsAffected), true)
		c.JSON(200, gin.H{"deleted": res.RowsAffected})
	})

	// ---------- 数据管理 ----------
	auth.GET("/settings/data/stats", func(c *gin.Context) {
		var historyRows, auditRows, alertRows int64
		db.Model(&MonitorHistory{}).Count(&historyRows)
		db.Model(&AuditLog{}).Count(&auditRows)
		db.Model(&AlertEvent{}).Count(&alertRows)

		var oldest, newest MonitorHistory
		db.Order("created_at ASC").Limit(1).Find(&oldest)
		db.Order("created_at DESC").Limit(1).Find(&newest)

		var byType = make([]struct {
			Type  string `json:"type"`
			Count int64  `json:"count"`
		}, 0)
		db.Model(&MonitorHistory{}).Select("type, count(*) as count").Group("type").Scan(&byType)

		globalConfig.RLock()
		keepHours := globalConfig.HistoryKeepHours
		auditDays := globalConfig.AuditKeepDays
		alertDays := globalConfig.AlertKeepDays
		globalConfig.RUnlock()

		res := gin.H{
			"db_path":         databasePath(),
			"db_size":         databaseSize(),
			"history_rows":    historyRows,
			"audit_rows":      auditRows,
			"alert_rows":      alertRows,
			"by_type":         byType,
			"history_keep":    keepHours,
			"audit_keep_days": auditDays,
			"alert_keep_days": alertDays,
			"oldest":          "",
			"newest":          "",
		}
		if !oldest.CreatedAt.IsZero() {
			res["oldest"] = oldest.CreatedAt.Format("2006-01-02 15:04:05")
		}
		if !newest.CreatedAt.IsZero() {
			res["newest"] = newest.CreatedAt.Format("2006-01-02 15:04:05")
		}
		c.JSON(200, res)
	})

	auth.POST("/settings/data/retention", func(c *gin.Context) {
		hours, err1 := strconv.Atoi(c.PostForm("history_hours"))
		auditDays, err2 := strconv.Atoi(c.PostForm("audit_days"))
		alertDays, err3 := strconv.Atoi(c.PostForm("alert_days"))
		if err1 != nil || err2 != nil || err3 != nil {
			c.String(400, "参数格式不正确")
			return
		}
		// 下限 1：写 0 会让清理任务每次都把全部历史删掉
		if hours < 1 || hours > 8760 {
			c.String(400, "监控历史保留时长需在 1 ~ 8760 小时之间")
			return
		}
		if auditDays < 1 || auditDays > 3650 {
			c.String(400, "操作日志保留天数需在 1 ~ 3650 天之间")
			return
		}
		if alertDays < 1 || alertDays > 3650 {
			c.String(400, "告警历史保留天数需在 1 ~ 3650 天之间")
			return
		}

		saveConfig("history_keep_hours", strconv.Itoa(hours))
		saveConfig("audit_keep_days", strconv.Itoa(auditDays))
		saveConfig("alert_keep_days", strconv.Itoa(alertDays))
		globalConfig.Lock()
		globalConfig.HistoryKeepHours = hours
		globalConfig.AuditKeepDays = auditDays
		globalConfig.AlertKeepDays = alertDays
		globalConfig.Unlock()

		audit(c, "retention_save", "",
			fmt.Sprintf("保留策略：监控历史 %d 小时 / 操作日志 %d 天 / 告警历史 %d 天", hours, auditDays, alertDays), true)
		c.Status(200)
	})

	auth.POST("/settings/data/cleanup", func(c *gin.Context) {
		mode := c.PostForm("mode")
		var deleted int64
		switch mode {
		case "all":
			res := db.Where("1 = 1").Delete(&MonitorHistory{})
			deleted = res.RowsAffected
			audit(c, "data_cleanup", "", fmt.Sprintf("清空全部监控历史，删除 %d 条", deleted), true)
		case "alerts":
			res := db.Where("1 = 1").Delete(&AlertEvent{})
			deleted = res.RowsAffected
			audit(c, "data_cleanup", "", fmt.Sprintf("清空告警历史，删除 %d 条", deleted), true)
		default:
			deleted = cleanupExpiredHistory()
			audit(c, "data_cleanup", "", fmt.Sprintf("按保留策略清理监控历史，删除 %d 条", deleted), true)
		}
		c.JSON(200, gin.H{"deleted": deleted})
	})

	auth.GET("/settings/data/export", func(c *gin.Context) {
		kind := c.DefaultQuery("type", "history")
		format := c.DefaultQuery("format", "json")

		// 直接用 [][]string 而不是固定结构体：
		// 三种导出的列数并不相同（5 / 6 / 7 列），共用一个结构体时
		// 很容易出现「表头 7 列、数据只写了 6 个」这种错位。
		var (
			name    string
			header  []string
			records [][]string
			objects []map[string]interface{}
		)

		switch kind {
		case "audit":
			name = "audit"
			header = []string{"时间", "操作者", "来源 IP", "动作", "对象", "详情", "结果"}
			var list []AuditLog
			db.Order("created_at DESC").Limit(100000).Find(&list)
			for _, v := range list {
				okText := "成功"
				if !v.Success {
					okText = "失败"
				}
				ts := v.CreatedAt.Format("2006-01-02 15:04:05")
				records = append(records, []string{ts, v.Username, v.IP, v.Action, v.Target, v.Detail, okText})
				objects = append(objects, map[string]interface{}{
					"time": ts, "username": v.Username, "ip": v.IP,
					"action": v.Action, "target": v.Target, "detail": v.Detail, "success": v.Success,
				})
			}
		case "alert":
			name = "alerts"
			header = []string{"时间", "节点 ID", "节点名称", "类型", "级别", "内容"}
			var list []AlertEvent
			db.Order("created_at DESC").Limit(100000).Find(&list)
			for _, v := range list {
				ts := v.CreatedAt.Format("2006-01-02 15:04:05")
				records = append(records, []string{ts, v.AgentID, v.NodeName, kindLabelOf(v.Kind), levelLabelOf(v.Level), v.Message})
				objects = append(objects, map[string]interface{}{
					"time": ts, "agent_id": v.AgentID, "node_name": v.NodeName,
					"kind": v.Kind, "level": v.Level, "message": v.Message,
				})
			}
		default:
			name = "history"
			header = []string{"时间", "节点 ID", "类型", "目标", "数值"}
			var list []MonitorHistory
			db.Order("created_at DESC").Limit(200000).Find(&list)
			for _, v := range list {
				ts := v.CreatedAt.Format("2006-01-02 15:04:05")
				val := strconv.FormatFloat(v.Value, 'f', -1, 64)
				records = append(records, []string{ts, v.AgentID, v.Type, v.Target, val})
				objects = append(objects, map[string]interface{}{
					"time": ts, "agent_id": v.AgentID, "type": v.Type,
					"target": v.Target, "value": v.Value,
				})
			}
		}

		stamp := time.Now().Format("20060102-150405")
		if format == "csv" {
			filename := fmt.Sprintf("monitor-%s-%s.csv", name, stamp)
			c.Header("Content-Type", "text/csv; charset=utf-8")
			c.Header("Content-Disposition", "attachment; filename=\""+filename+"\"")
			// 写 UTF-8 BOM：不加的话 Excel 会把中文表头显示成乱码
			_, _ = c.Writer.Write([]byte{0xEF, 0xBB, 0xBF})
			w := csv.NewWriter(c.Writer)
			_ = w.Write(header)
			for _, r := range records {
				_ = w.Write(r)
			}
			w.Flush()
			audit(c, "data_export", name, fmt.Sprintf("导出 %s（CSV，%d 条）", name, len(records)), true)
			return
		}

		filename := fmt.Sprintf("monitor-%s-%s.json", name, stamp)
		c.Header("Content-Disposition", "attachment; filename=\""+filename+"\"")
		audit(c, "data_export", name, fmt.Sprintf("导出 %s（JSON，%d 条）", name, len(objects)), true)
		c.JSON(200, gin.H{
			"type":        name,
			"exported_at": time.Now().Format(time.RFC3339),
			"count":       len(objects),
			"rows":        objects,
		})
	})

	// 在线备份：不用停服务，也不会拷到半截数据
	auth.GET("/settings/data/backup", func(c *gin.Context) {
		tmpDir, err := os.MkdirTemp("", "hm-backup-")
		if err != nil {
			c.String(500, "无法创建临时目录："+err.Error())
			return
		}
		defer os.RemoveAll(tmpDir)

		filename := "monitor-backup-" + time.Now().Format("20060102-150405") + ".db"
		dest := filepath.Join(tmpDir, filename)

		// VACUUM INTO 是 SQLite 原生的在线备份原语：它在读事务里取快照，
		// 产出一个已经合并好 WAL 的自洽单文件。
		//
		// 为什么不直接 cp monitor.db：开了 WAL 之后，新提交的事务先写 -wal，
		// 主文件在 checkpoint 之前可能【根本没有最新数据】—— 直接拷主文件
		// 有机会得到一个空库，而使用者要到恢复那天才会发现。
		//
		// 另外 VACUUM 语句不接受绑定参数，路径只能拼成 SQL 字面量，
		// 因此单引号必须翻倍转义。
		quoted := "'" + strings.ReplaceAll(filepath.ToSlash(dest), "'", "''") + "'"
		if err := db.Exec("VACUUM INTO " + quoted).Error; err != nil {
			audit(c, "db_backup", "", "备份失败："+err.Error(), false)
			c.String(500, "备份失败："+err.Error())
			return
		}

		// 生成文件 ≠ 拿到可用的备份。必须回读校验一遍，
		// 否则「以为成功了、真要用时才发现是空的」才是最糟的结局。
		summary, err := inspectBackup(dest)
		if err != nil {
			audit(c, "db_backup", "", "备份校验失败："+err.Error(), false)
			c.String(500, "备份文件校验未通过："+err.Error())
			return
		}
		audit(c, "db_backup", "", summary, true)

		c.FileAttachment(dest, filename)
	})

	// ---------- 节点批量操作 ----------
	auth.POST("/settings/nodes/batch", func(c *gin.Context) {
		var req struct {
			Action string   `json:"action"`
			IDs    []string `json:"ids"`
			Value  string   `json:"value"`
		}
		if err := c.ShouldBindJSON(&req); err != nil || len(req.IDs) == 0 {
			c.JSON(400, gin.H{"error": "请求参数不完整"})
			return
		}
		if len(req.IDs) > 500 {
			c.JSON(400, gin.H{"error": "单次最多操作 500 个节点"})
			return
		}
		ids := make([]string, 0, len(req.IDs))
		for _, id := range req.IDs {
			if v := cleanField(id, 64); v != "" {
				ids = append(ids, v)
			}
		}
		if len(ids) == 0 {
			c.JSON(400, gin.H{"error": "没有有效的节点 ID"})
			return
		}

		var affected int64
		switch req.Action {
		case "delete":
			affected = db.Model(&Node{}).Where("agent_id IN ?", ids).Update("denied", true).RowsAffected
			cacheMutex.Lock()
			for _, id := range ids {
				delete(statusCache, id)
			}
			cacheMutex.Unlock()
			for _, id := range ids {
				clearNodeAlertState(id)
			}
		case "hide":
			affected = db.Model(&Node{}).Where("agent_id IN ?", ids).Update("hide_id", true).RowsAffected
		case "unhide":
			affected = db.Model(&Node{}).Where("agent_id IN ?", ids).Update("hide_id", false).RowsAffected
		case "group":
			g := cleanField(req.Value, 32)
			affected = db.Model(&Node{}).Where("agent_id IN ?", ids).Update("group", g).RowsAffected
			req.Value = g
		case "maintenance_on":
			affected = db.Model(&Node{}).Where("agent_id IN ?", ids).Update("maintenance", true).RowsAffected
		case "maintenance_off":
			affected = db.Model(&Node{}).Where("agent_id IN ?", ids).Update("maintenance", false).RowsAffected
		case "mute_on":
			affected = db.Model(&Node{}).Where("agent_id IN ?", ids).Update("alert_muted", true).RowsAffected
		case "mute_off":
			affected = db.Model(&Node{}).Where("agent_id IN ?", ids).Update("alert_muted", false).RowsAffected
		case "update":
			n, err := pushAgentUpdateBatch(ids)
			if err != nil {
				c.JSON(200, gin.H{"error": err.Error()})
				return
			}
			audit(c, "agent_push_batch", fmt.Sprintf("%d 个节点", len(ids)),
				fmt.Sprintf("批量下发客户端更新，命中 %d 个节点", n), true)
			c.JSON(200, gin.H{"count": n})
			return
		default:
			c.JSON(400, gin.H{"error": "不支持的操作：" + req.Action})
			return
		}

		audit(c, "node_batch", fmt.Sprintf("%d 个节点", len(ids)),
			fmt.Sprintf("批量操作 %s（影响 %d 个节点）%s", req.Action, affected, req.Value), true)
		c.JSON(200, gin.H{"count": affected})
	})

	// 单个节点的分组 / 维护 / 静音
	auth.POST("/settings/node_meta", func(c *gin.Context) {
		id := cleanField(c.PostForm("id"), 64)
		if id == "" {
			c.Status(400)
			return
		}
		upd := map[string]interface{}{}
		if v, ok := c.GetPostForm("group"); ok {
			upd["group"] = cleanField(v, 32)
		}
		if v, ok := c.GetPostForm("maintenance"); ok {
			upd["maintenance"] = v == "1" || v == "true"
		}
		if v, ok := c.GetPostForm("alert_muted"); ok {
			upd["alert_muted"] = v == "1" || v == "true"
		}
		if len(upd) == 0 {
			c.Status(400)
			return
		}
		if err := db.Model(&Node{}).Where("agent_id = ?", id).Updates(upd).Error; err != nil {
			c.Status(500)
			return
		}
		audit(c, "node_meta", id, fmt.Sprintf("更新节点标记：%v", upd), true)
		c.Status(200)
	})

	// 分组清单（供前端做筛选与自动补全）
	auth.GET("/settings/node_groups", func(c *gin.Context) {
		// 不做 SQL 层 DISTINCT：group 是 SQL 保留字，拼进 Pluck/Order 里
		// 很容易踩到引号转义的坑（实测会静默返回空集）。
		// 节点数量本就不大，取回来在内存里去重更稳。
		var raw []string
		db.Model(&Node{}).Where("denied = ?", false).Pluck("COALESCE(\"group\", '')", &raw)

		seen := make(map[string]bool, len(raw))
		out := make([]string, 0, len(raw))
		for _, g := range raw {
			g = strings.TrimSpace(g)
			if g == "" || seen[g] {
				continue
			}
			seen[g] = true
			out = append(out, g)
		}
		sort.Strings(out)
		c.JSON(200, out)
	})
}

// pushAgentUpdateBatch 给指定的一批节点打上待更新标记
func pushAgentUpdateBatch(ids []string) (int, error) {
	bundle := getAgentBundleVersion()
	if bundle == "" || len(cachedArchList()) == 0 {
		return 0, fmt.Errorf("请先在「版本更新」中同步客户端程序")
	}
	res := db.Model(&Node{}).Where("agent_id IN ?", ids).Update("pending_update", bundle)
	return int(res.RowsAffected), res.Error
}

// ================= 备份校验 =================

// inspectBackup 回读刚生成的备份文件，做完整性检查与行数统计。
// 返回一句人话总结，直接写进审计日志。
func inspectBackup(path string) (string, error) {
	st, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if st.Size() < 4096 {
		return "", fmt.Errorf("文件仅 %d 字节，明显不完整", st.Size())
	}

	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	hdr := make([]byte, 16)
	_, err = io.ReadFull(f, hdr)
	f.Close()
	if err != nil {
		return "", err
	}
	if string(hdr) != "SQLite format 3\x00" {
		return "", fmt.Errorf("文件头不是 SQLite 格式")
	}

	tdb, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	if err != nil {
		return "", err
	}
	sqlDB, err := tdb.DB()
	if err != nil {
		return "", err
	}
	defer sqlDB.Close()

	var integrity string
	if err := tdb.Raw("PRAGMA integrity_check").Scan(&integrity).Error; err != nil {
		return "", err
	}
	if !strings.EqualFold(strings.TrimSpace(integrity), "ok") {
		return "", fmt.Errorf("完整性检查未通过：%s", integrity)
	}

	var users, nodes, hist int64
	tdb.Table("users").Count(&users)
	tdb.Table("nodes").Count(&nodes)
	tdb.Table("monitor_histories").Count(&hist)

	return fmt.Sprintf("备份完成：%s，完整性 ok，账号 %d、节点 %d、监控历史 %d 条",
		humanSize(st.Size()), users, nodes, hist), nil
}

// ================= 后台维护任务 =================

// cleanupExpiredHistory 按保留策略清理监控历史
func cleanupExpiredHistory() int64 {
	globalConfig.RLock()
	hours := globalConfig.HistoryKeepHours
	globalConfig.RUnlock()
	if hours <= 0 {
		hours = 24
	}
	res := db.Where("created_at < ?", time.Now().Add(-time.Duration(hours)*time.Hour)).
		Delete(&MonitorHistory{})
	return res.RowsAffected
}

func cleanupExpiredAlerts() int64 {
	globalConfig.RLock()
	days := globalConfig.AlertKeepDays
	globalConfig.RUnlock()
	if days <= 0 {
		days = 30
	}
	res := db.Where("created_at < ?", time.Now().AddDate(0, 0, -days)).Delete(&AlertEvent{})
	return res.RowsAffected
}

func cleanupExpiredAudit() int64 {
	globalConfig.RLock()
	days := globalConfig.AuditKeepDays
	globalConfig.RUnlock()
	if days <= 0 {
		days = 90
	}
	res := db.Where("created_at < ?", time.Now().AddDate(0, 0, -days)).Delete(&AuditLog{})
	return res.RowsAffected
}

// cleanupMaintenance 后台维护循环。
//
// 拆成两个频率：登录失败计数需要勤快清理（内存里的短期状态），
// 历史数据清理没必要每分钟都去扫一遍整张表。
func cleanupMaintenance() {
	pruneTicker := time.NewTicker(5 * time.Minute)
	hourTicker := time.NewTicker(time.Hour)
	defer pruneTicker.Stop()
	defer hourTicker.Stop()

	// 启动后先等一会儿再跑首轮，避开 AutoMigrate 与首次配置加载
	go func() {
		time.Sleep(90 * time.Second)
		cleanupExpiredHistory()
		cleanupExpiredAlerts()
		cleanupExpiredAudit()
	}()

	for {
		select {
		case <-pruneTicker.C:
			pruneLoginGuard()
		case <-hourTicker.C:
			cleanupExpiredHistory()
			cleanupExpiredAlerts()
			cleanupExpiredAudit()
		}
	}
}
