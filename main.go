package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"io/ioutil"
	"net"
	"net/http"
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
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ================= 全局配置 =================

type PingTargetConfig struct {
	Target string `json:"target"`
	Alias  string `json:"alias"`
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
	}

	alertState = make(map[string]bool)
)

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

// ================= 服务端 =================

func runServer(port string) {
	var err error
	db, err = gorm.Open(sqlite.Open("monitor.db"), &gorm.Config{})
	if err != nil {
		panic(err)
	}

	db.AutoMigrate(&User{}, &AppConfig{}, &Node{}, &MonitorHistory{})
	loadGlobalConfig()
	go monitorAlerts()
	go cleanupHistory()

	gin.SetMode(gin.ReleaseMode)
	r := gin.Default()

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
		})
	})
	r.POST("/setup", func(c *gin.Context) {
		u, p := c.PostForm("username"), c.PostForm("password")
		if u != "" && p != "" {
			db.Create(&User{Username: u, Password: hashPwd(p)})
			c.Redirect(302, "/login")
		}
	})
	r.GET("/login", func(c *gin.Context) {
		var cnt int64
		db.Model(&User{}).Count(&cnt)
		if cnt == 0 {
			c.Redirect(302, "/setup")
			return
		}
		if sessions.Default(c).Get("user") != nil {
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
		})
	})
	r.POST("/login", func(c *gin.Context) {
		u, p := c.PostForm("username"), c.PostForm("password")
		var user User
		// 安全增强: 校验加盐哈希
		if db.Where("username=?", u).First(&user).Error == nil {
			if checkPwd(p, user.Password) {
				s := sessions.Default(c)
				s.Set("user", u)
				s.Save()
				c.Redirect(302, "/")
				return
			}
		}
		c.Redirect(302, "/login")
	})
	r.GET("/logout", func(c *gin.Context) { s := sessions.Default(c); s.Clear(); s.Save(); c.Redirect(302, "/") })

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
					go func(aid, ip string) {
						resp, err := http.Get("http://ip-api.com/json/" + ip)
						if err == nil {
							defer resp.Body.Close()
							var res struct {
								CountryCode string `json:"countryCode"`
							}
							if json.NewDecoder(resp.Body).Decode(&res) == nil && res.CountryCode != "" {
								db.Model(&Node{}).Where("agent_id=?", aid).Update("country_code", res.CountryCode)
							}
						}
					}(s.AgentID, s.IP)
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
			isAdmin := sessions.Default(c).Get("user") != nil

			// 1. 获取所有数据库中的节点
			var nodes []Node
			db.Find(&nodes)

			cacheMutex.RLock()
			defer cacheMutex.RUnlock()
			res := make(map[string]SystemStatus)

			for _, n := range nodes {
				if n.Denied {
					continue
				}

				// 2. 优先读取缓存中的实时数据
				if v, ok := statusCache[n.AgentID]; ok {
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

				c.JSON(200, gin.H{
					"status": "ok",
					"id":     id,
					"cmd":    cmd,
				})
			})

			auth.POST("/settings/token", func(c *gin.Context) {
				t := c.PostForm("token")
				if len(t) < 3 {
					c.Status(400)
					return
				}
				saveConfig("token", t)
				globalConfig.Lock()
				globalConfig.Token = t
				globalConfig.Unlock()
				c.Status(200)
			})
			auth.POST("/settings/url", func(c *gin.Context) {
				u := c.PostForm("url")
				saveConfig("server_url", u)
				globalConfig.Lock()
				globalConfig.ServerURL = u
				globalConfig.Unlock()
				c.Status(200)
			})
			auth.POST("/settings/alert", func(c *gin.Context) {
				tk, ch, wh := c.PostForm("token"), c.PostForm("chat"), c.PostForm("webhook")
				saveConfig("tg_token", tk)
				saveConfig("tg_chat", ch)
				saveConfig("webhook_url", wh)
				globalConfig.Lock()
				globalConfig.TGToken = tk
				globalConfig.TGChatID = ch
				globalConfig.WebhookURL = wh
				globalConfig.Unlock()
				c.Status(200)
			})
			auth.POST("/settings/test_alert", func(c *gin.Context) {
				sendAlert("🔔 测试告警消息\nMonitor 配置成功！")
				c.Status(200)
			})
			auth.POST("/settings/update_node", func(c *gin.Context) {
				id := c.PostForm("id")
				name := c.PostForm("name")
				sort, _ := strconv.Atoi(c.PostForm("sort"))
				db.Model(&Node{}).Where("agent_id=?", id).Updates(map[string]interface{}{"name": name, "sort_order": sort})
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
					b, _ := json.Marshal(targets)
					saveConfig("sys_ping_targets", string(b))
					globalConfig.Lock()
					globalConfig.PingTargets = targets
					globalConfig.Unlock()
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
				db.Model(&Node{}).Where("agent_id=?", id).Update("denied", true)
				cacheMutex.Lock()
				delete(statusCache, id)
				cacheMutex.Unlock()
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
				startAgentSync()
				c.Status(200)
			})

			// 下发客户端更新指令（id=all 表示全部）
			auth.POST("/settings/update/agent_push", func(c *gin.Context) {
				n, err := pushAgentUpdate(c.PostForm("id"))
				if err != nil {
					c.JSON(200, gin.H{"error": err.Error()})
					return
				}
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
				saveConfig("update_repo", repo)
				saveConfig("update_proxy", proxy)
				saveConfig("restart_cmd", cmd)
				globalConfig.Lock()
				globalConfig.UpdateRepo = repo
				globalConfig.UpdateProxy = proxy
				globalConfig.RestartCmd = cmd
				globalConfig.Unlock()
				c.Status(200)
			})
		}
	}

	fmt.Printf(">> http://localhost:%s\n", port)
	r.Run(":" + port)
}

func monitorAlerts() {
	for {
		time.Sleep(5 * time.Second)
		cacheMutex.RLock()
		for id, s := range statusCache {
			isOffline := time.Since(s.LastUpdate) > 30*time.Second
			alreadyAlerted := alertState[id]
			if isOffline && !alreadyAlerted {
				alertState[id] = true
				sendAlert(fmt.Sprintf("🔴 节点离线告警\nID: %s\nName: %s\nIP: %s", s.AgentID, s.Name, s.IP))
			} else if !isOffline && alreadyAlerted {
				alertState[id] = false
				sendAlert(fmt.Sprintf("🟢 节点恢复上线\nID: %s\nName: %s", s.AgentID, s.Name))
			}
		}
		cacheMutex.RUnlock()
	}
}

func cleanupHistory() {
	for {
		time.Sleep(1 * time.Hour)
		db.Where("created_at < ?", time.Now().Add(-24*time.Hour)).Delete(&MonitorHistory{})
	}
}

func sendAlert(msg string) {
	globalConfig.RLock()
	token := globalConfig.TGToken
	chat := globalConfig.TGChatID
	wh := globalConfig.WebhookURL
	globalConfig.RUnlock()

	// Telegram
	if token != "" && chat != "" {
		http.PostForm(fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", token), map[string][]string{"chat_id": {chat}, "text": {msg}})
	}

	// Webhook (JSON)
	if wh != "" {
		payload := map[string]string{"content": msg, "text": msg} // 兼容 discord/dingtalk
		b, _ := json.Marshal(payload)
		http.Post(wh, "application/json", bytes.NewBuffer(b))
	}
}

func loadGlobalConfig() {
	var cfgs []AppConfig
	db.Find(&cfgs)
	globalConfig.Token = "default-token"
	globalConfig.BgType = "default"
	globalConfig.CardOpacity = 0.9   // 默认值
	globalConfig.CardPadding = 10    // 默认值
	globalConfig.SiteTheme = "light" // 默认亮色
	defaultTargets := []PingTargetConfig{{Target: "8.8.8.8:53", Alias: "Google DNS"}}

	for _, c := range cfgs {
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
		}
	}
	if len(globalConfig.PingTargets) == 0 {
		globalConfig.PingTargets = defaultTargets
	}
	if len(cfgs) == 0 {
		saveConfig("token", "default-token")
	}
}

func saveConfig(k, v string) {
	db.Clauses(clause.OnConflict{UpdateAll: true}).Create(&AppConfig{Key: k, Value: v})
}

func dashboardHandler(c *gin.Context) {
	s := sessions.Default(c)
	isAdmin := s.Get("user") != nil
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

	t.Execute(c.Writer, map[string]interface{}{
		"BrowserURL": sch + c.Request.Host, "CustomServerURL": u, "Token": tk, "TGToken": tgt, "TGChatID": tgc, "WebhookURL": wh,
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
		if sessions.Default(c).Get("user") == nil {
			c.Redirect(302, "/login")
			c.Abort()
			return
		}
		c.Next()
	}
}

// 安全增强: 加盐哈希
func hashPwd(password string) string {
	salt := make([]byte, 16)
	rand.Read(salt)
	hash := sha256.Sum256(append(salt, []byte(password)...))
	return hex.EncodeToString(salt) + "$" + hex.EncodeToString(hash[:])
}

// 安全增强: 校验密码
func checkPwd(password, stored string) bool {
	parts := strings.Split(stored, "$")
	if len(parts) != 2 {
		return false
	}
	salt, _ := hex.DecodeString(parts[0])
	expectedHash, _ := hex.DecodeString(parts[1])
	actualHash := sha256.Sum256(append(salt, []byte(password)...))
	return subtle.ConstantTimeCompare(expectedHash, actualHash[:]) == 1
}

// ================= Agent =================

func runAgent(server, token, id string) {
	fmt.Printf("Agent -> %s (ID:%s)\n", server, id)
	url := fmt.Sprintf("%s/api/report", server) // 移除 URL 参数
	client := &http.Client{Timeout: 5 * time.Second}

	hostInfo, _ := host.Info()
	osInfo := fmt.Sprintf("%s %s", hostInfo.Platform, hostInfo.PlatformVersion)

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
			CPUUsage: cVal, MemUsedPercent: vm.UsedPercent, DiskUsedPercent: du.UsedPercent,
			NetInSpeed: spIn, NetOutSpeed: spOut, NetTotalIn: curIn, NetTotalOut: curOut,
			PingResults: latestPingResults,
			Version:     BuildVersion,   // [新增] 上报自身版本，供面板判断是否需要更新
			Arch:        runtime.GOARCH, // [新增] 上报架构，用于分发对应二进制
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
