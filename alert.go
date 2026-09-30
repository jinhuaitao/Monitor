package main

// ================= 告警子系统 =================
//
// 与早期版本的区别：原先只有「离线 / 恢复」一种告警，判定时长、轮询间隔、
// 通知文案全部硬编码在循环里，也没有任何历史记录。这里把它升级成一套
// 可配置的规则引擎，并补上告警落库 —— 监控系统最怕的不是误报，
// 而是「昨晚到底报过什么」事后查不到。
//
// 三个关键设计：
//
//  1. 每条规则（节点 × 类型）独立维护「是否处于告警中」与「上次发送时间」，
//     用状态翻转驱动通知。若每次检测都发，一个离线三天的节点会刷出五万条消息。
//
//  2. 冷却窗口只约束【重复提醒】，不约束【恢复通知】。恢复消息必须立刻发出去，
//     否则运维会一直以为还在故障中。cooldown = 0 表示"仅在状态变化时通知一次"。
//
//  3. 维护模式与静音开关在检测阶段就短路，避免"先算完再丢掉"的白工。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// onlineWindow 判定「节点在线」的时间窗口。
// 同时被告警引擎与系统信息统计复用，避免两处各写一个魔数导致口径不一致。
const onlineWindow = 30 * time.Second

// alertClient 带超时的 HTTP 客户端。
// 原实现直接用 http.Post / http.PostForm（默认客户端无超时），
// 一个挂住的 webhook 地址能把告警循环永久堵死。
var alertClient = &http.Client{Timeout: 10 * time.Second}

// ================= 规则 =================

// AlertRule 一份规则快照。检测循环每轮取一次快照，
// 避免在遍历过程中反复加锁读配置。
type AlertRule struct {
	Enabled     bool    `json:"enabled"`
	Offline     bool    `json:"offline"`
	OfflineSec  int     `json:"offline_sec"`
	CooldownMin int     `json:"cooldown_min"`
	CPU         float64 `json:"cpu"`
	Mem         float64 `json:"mem"`
	Disk        float64 `json:"disk"`
	KeepDays    int     `json:"keep_days"`
}

func alertRuleSnapshot() AlertRule {
	globalConfig.RLock()
	defer globalConfig.RUnlock()
	return AlertRule{
		Enabled:     globalConfig.AlertEnabled,
		Offline:     globalConfig.AlertOffline,
		OfflineSec:  globalConfig.AlertOfflineSec,
		CooldownMin: globalConfig.AlertCooldownMin,
		CPU:         globalConfig.AlertCPU,
		Mem:         globalConfig.AlertMem,
		Disk:        globalConfig.AlertDisk,
		KeepDays:    globalConfig.AlertKeepDays,
	}
}

// ================= 状态机 =================

type alertStateEntry struct {
	active   bool
	lastSent time.Time
}

var alertStates = struct {
	sync.Mutex
	m map[string]*alertStateEntry
}{m: make(map[string]*alertStateEntry)}

func alertStateKey(agentID, kind string) string { return agentID + "|" + kind }

// fireAlert 把某条规则置为「告警中」。
//
// 是否真正发送由两个条件决定：
//   - 状态刚刚翻转（上一次不在告警中）→ 必发
//   - 冷却窗口已过且 cooldown > 0 → 重复提醒
//
// cooldown = 0 明确表示「只在状态变化时通知一次」，而不是"每轮都发"。
// 早期版本如果把 0 当作"立即冷却完毕"，就会退化成 5 秒一条的轰炸。
func fireAlert(agentID, nodeName, kind, level, msg string, cooldownMin int) {
	key := alertStateKey(agentID, kind)

	alertStates.Lock()
	e := alertStates.m[key]
	if e == nil {
		e = &alertStateEntry{}
		alertStates.m[key] = e
	}
	wasActive := e.active
	repeat := cooldownMin > 0 &&
		(e.lastSent.IsZero() || time.Since(e.lastSent) >= time.Duration(cooldownMin)*time.Minute)
	e.active = true
	if !wasActive || repeat {
		e.lastSent = time.Now()
	}
	alertStates.Unlock()

	if wasActive && !repeat {
		return
	}
	sendAlert(msg)
	recordAlertEvent(agentID, nodeName, kind, level, msg)
}

// resolveAlert 解除某条规则。
// 恢复通知不受冷却窗口限制 —— 故障结束了必须立刻让人知道。
func resolveAlert(agentID, nodeName, kind, msg string) {
	key := alertStateKey(agentID, kind)

	alertStates.Lock()
	e := alertStates.m[key]
	wasActive := e != nil && e.active
	if e != nil {
		e.active = false
	}
	alertStates.Unlock()

	if !wasActive {
		return
	}
	sendAlert(msg)
	recordAlertEvent(agentID, nodeName, kind, "info", msg)
}

// clearNodeAlertState 节点被删除后清掉它的状态，防止 map 无限增长
func clearNodeAlertState(agentID string) {
	alertStates.Lock()
	for _, kind := range []string{"offline", "cpu", "mem", "disk"} {
		delete(alertStates.m, alertStateKey(agentID, kind))
	}
	alertStates.Unlock()
}

func recordAlertEvent(agentID, nodeName, kind, level, msg string) {
	if db == nil {
		return
	}
	ev := AlertEvent{
		CreatedAt: time.Now(),
		AgentID:   cleanField(agentID, 64),
		NodeName:  cleanField(nodeName, 64),
		Kind:      cleanField(kind, 16),
		Level:     cleanField(level, 16),
		// 用 cleanMultiline 而不是 cleanField：告警文案本身是多行的，
		// 前端会按 \n 拆开用 " · " 连接。cleanField 会把换行当控制字符删掉，
		// 于是历史记录里所有字段粘成一整串，分隔符永远不会出现。
		Message: cleanMultiline(msg, 500),
	}
	if err := db.Create(&ev).Error; err != nil {
		log.Printf("[alert] 事件落库失败: %v", err)
	}
}

// ================= 检测循环 =================

func monitorAlerts() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		runAlertScan()
	}
}

func runAlertScan() {
	rule := alertRuleSnapshot()
	if !rule.Enabled {
		return
	}

	// 快照实时数据，不要在持有读锁的情况下做网络请求或写库
	cacheMutex.RLock()
	snapshot := make([]SystemStatus, 0, len(statusCache))
	for _, v := range statusCache {
		snapshot = append(snapshot, v)
	}
	cacheMutex.RUnlock()
	if len(snapshot) == 0 {
		return
	}

	// 取节点元信息：名称、维护模式、静音开关、删除标记
	var nodes []Node
	if db == nil {
		return
	}
	db.Find(&nodes)
	meta := make(map[string]Node, len(nodes))
	for _, n := range nodes {
		meta[n.AgentID] = n
	}

	seen := make(map[string]bool, len(snapshot))
	for _, s := range snapshot {
		n := meta[s.AgentID]
		seen[s.AgentID] = true

		// 已删除的节点不再告警，并顺手清掉残留状态
		if n.Denied {
			clearNodeAlertState(s.AgentID)
			continue
		}

		display := s.Name
		if display == "" {
			display = n.Name
		}
		if display == "" {
			display = s.AgentID
		}

		// 维护模式 / 静音：直接跳过检测。
		// 这里刻意【不】清除已有告警状态 —— 否则维护结束的那一刻会立刻
		// 重新触发一轮"离线告警"，比不静音还吵。
		if n.Maintenance || n.AlertMuted {
			continue
		}

		offline := time.Since(s.LastUpdate) > time.Duration(rule.OfflineSec)*time.Second

		if rule.Offline {
			if offline {
				secs := int(time.Since(s.LastUpdate).Seconds())
				fireAlert(s.AgentID, display, "offline", "critical",
					fmt.Sprintf("🔴 节点离线\n名称：%s\nID：%s\nIP：%s\n已失联：%s",
						display, s.AgentID, displayIP(s.IP), humanDuration(secs)),
					rule.CooldownMin)
			} else {
				resolveAlert(s.AgentID, display, "offline",
					fmt.Sprintf("🟢 节点恢复上线\n名称：%s\nID：%s", display, s.AgentID))
			}
		}

		// 资源阈值只在节点在线时有意义：离线节点的 CPU 是上一次的残留值
		if offline {
			continue
		}
		checkThreshold(s.AgentID, display, "cpu", "CPU 使用率", s.CPUUsage, rule.CPU, rule.CooldownMin)
		checkThreshold(s.AgentID, display, "mem", "内存使用率", s.MemUsedPercent, rule.Mem, rule.CooldownMin)
		checkThreshold(s.AgentID, display, "disk", "磁盘使用率", s.DiskUsedPercent, rule.Disk, rule.CooldownMin)
	}

	pruneAlertStates(seen)
}

// pruneAlertStates 清掉「已经不在缓存里」的节点的告警状态（比如节点被删除后
// 面板重启过），防止 alertStates 这个 map 只增不减。
//
// seen 是本轮仍然存在的节点 ID 集合。
//
// 单独抽成函数是为了能被直接测到：下面这个键拆分必须用 LastIndex，
// 而 runAlertScan 需要数据库与实时缓存才能跑起来，很难单独构造场景。
func pruneAlertStates(seen map[string]bool) {
	alertStates.Lock()
	defer alertStates.Unlock()
	for key, e := range alertStates.m {
		// 状态键是 agentID + "|" + kind，而 kind 只可能是
		// offline / cpu / mem / disk 这四个固定常量，绝不含 "|"。
		// 反过来 AgentID 来自 Agent 上报，无法保证不含 "|" ——
		// 若用 Index 按第一个 "|" 拆，ID 里一旦带 "|" 拆出来的就不是完整 ID，
		// 这些状态会永远匹配不上 seen，map 只增不减。
		idx := strings.LastIndex(key, "|")
		if idx < 0 {
			delete(alertStates.m, key)
			continue
		}
		if !seen[key[:idx]] && !e.active {
			delete(alertStates.m, key)
		}
	}
}

// checkThreshold 单条阈值规则。limit <= 0 视为关闭该规则。
func checkThreshold(agentID, nodeName, kind, label string, value, limit float64, cooldownMin int) {
	if limit <= 0 {
		// 规则被关掉时，把之前挂着的告警收掉，避免永远停在"告警中"
		resolveAlert(agentID, nodeName, kind,
			fmt.Sprintf("✅ %s 告警规则已关闭\n名称：%s\n当前：%.1f%%", label, nodeName, value))
		return
	}

	if value >= limit {
		level := "warning"
		if kind == "disk" && value >= 95 {
			level = "critical"
		}
		if value >= 98 {
			level = "critical"
		}
		icon := "⚠️"
		if level == "critical" {
			icon = "🚨"
		}
		fireAlert(agentID, nodeName, kind, level,
			fmt.Sprintf("%s %s 超过阈值\n名称：%s\n当前：%.1f%%\n阈值：%.0f%%",
				icon, label, nodeName, value, limit),
			cooldownMin)
		return
	}

	resolveAlert(agentID, nodeName, kind,
		fmt.Sprintf("✅ %s 恢复正常\n名称：%s\n当前：%.1f%%", label, nodeName, value))
}

func displayIP(ip string) string {
	if ip == "" || ip == "Hidden" {
		return "—"
	}
	return ip
}

// 导出 CSV 时把内部标识翻成中文，否则导出来的表格里全是 offline / cpu 这类键名
func kindLabelOf(kind string) string {
	switch kind {
	case "offline":
		return "离线"
	case "online":
		return "恢复"
	case "cpu":
		return "CPU"
	case "mem":
		return "内存"
	case "disk":
		return "磁盘"
	}
	return kind
}

func levelLabelOf(level string) string {
	switch level {
	case "critical":
		return "严重"
	case "warning":
		return "警告"
	case "info":
		return "恢复"
	}
	return level
}

func humanDuration(sec int) string {
	if sec < 60 {
		return fmt.Sprintf("%d 秒", sec)
	}
	if sec < 3600 {
		return fmt.Sprintf("%d 分 %d 秒", sec/60, sec%60)
	}
	return fmt.Sprintf("%d 小时 %d 分", sec/3600, (sec%3600)/60)
}

// ================= 通知通道 =================

// sendAlert 向所有已配置的通道推送消息，返回命中的通道数。
//
// 每个通道各起一个 goroutine：webhook 挂掉时单次请求可能卡满 10 秒超时，
// 若同步串行发送，20 个节点同时离线会把告警循环拖住好几分钟，
// 期间的告警全部堆积。
func sendAlert(msg string) int {
	globalConfig.RLock()
	token := globalConfig.TGToken
	chat := globalConfig.TGChatID
	wh := globalConfig.WebhookURL
	whFormat := globalConfig.WebhookFormat
	globalConfig.RUnlock()

	sent := 0

	if token != "" && chat != "" {
		sent++
		go func(t, c, m string) {
			resp, err := alertClient.PostForm(
				fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", t),
				url.Values{"chat_id": {c}, "text": {m}})
			if err != nil {
				log.Printf("[alert] Telegram 推送失败: %v", err)
				return
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}(token, chat, msg)
	}

	if wh != "" {
		sent++
		go func(u, format, m string) {
			body, err := buildWebhookPayload(format, m)
			if err != nil {
				log.Printf("[alert] 构造 Webhook 载荷失败: %v", err)
				return
			}
			req, err := http.NewRequest("POST", u, bytes.NewReader(body))
			if err != nil {
				log.Printf("[alert] Webhook 请求构造失败: %v", err)
				return
			}
			req.Header.Set("Content-Type", "application/json")
			resp, err := alertClient.Do(req)
			if err != nil {
				log.Printf("[alert] Webhook 推送失败: %v", err)
				return
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}(wh, whFormat, msg)
	}

	return sent
}

// buildWebhookPayload 按平台生成对应的消息体。
//
// 早期实现对所有平台都发 {"content":msg,"text":msg}，只有 Discord 恰好能收 ——
// 钉钉要求 text 是对象、飞书要求 content.text、Slack 只认 text。
// 让使用者显式选一次，比"猜平台"可靠得多。
func buildWebhookPayload(format, msg string) ([]byte, error) {
	switch format {
	case "dingtalk":
		return json.Marshal(map[string]interface{}{
			"msgtype": "text",
			"text":    map[string]string{"content": msg},
		})
	case "feishu":
		return json.Marshal(map[string]interface{}{
			"msg_type": "text",
			"content":  map[string]string{"text": msg},
		})
	case "discord":
		return json.Marshal(map[string]string{"content": msg})
	case "slack":
		return json.Marshal(map[string]string{"text": msg})
	default: // generic：同时带上 content 与 text，兼容大多数自定义接收端
		return json.Marshal(map[string]string{"content": msg, "text": msg})
	}
}

// ================= 路由 =================

func registerAlertRoutes(auth *gin.RouterGroup) {

	auth.GET("/settings/alert/rules", func(c *gin.Context) {
		c.JSON(200, alertRuleSnapshot())
	})

	auth.POST("/settings/alert/rules", func(c *gin.Context) {
		enabled := c.PostForm("enabled") == "1"
		offline := c.PostForm("offline") == "1"
		offlineSec, err1 := strconv.Atoi(c.PostForm("offline_sec"))
		cooldown, err2 := strconv.Atoi(c.PostForm("cooldown_min"))
		cpu, err3 := strconv.ParseFloat(c.PostForm("cpu"), 64)
		mem, err4 := strconv.ParseFloat(c.PostForm("mem"), 64)
		disk, err5 := strconv.ParseFloat(c.PostForm("disk"), 64)
		keepDays, err6 := strconv.Atoi(c.PostForm("keep_days"))

		if err1 != nil || err2 != nil || err3 != nil || err4 != nil || err5 != nil || err6 != nil {
			c.String(400, "参数格式不正确")
			return
		}
		// 离线判定不能小于告警轮询间隔，否则每次心跳间隙都会被判成离线，全线误报
		if offlineSec < 15 || offlineSec > 86400 {
			c.String(400, "离线判定时长需在 15 ~ 86400 秒之间")
			return
		}
		if cooldown < 0 || cooldown > 1440 {
			c.String(400, "重复提醒间隔需在 0 ~ 1440 分钟之间（0 表示只在状态变化时通知）")
			return
		}
		for _, v := range []float64{cpu, mem, disk} {
			if v < 0 || v > 100 {
				c.String(400, "资源阈值需在 0 ~ 100 之间（填 0 表示关闭该项告警）")
				return
			}
		}
		if keepDays < 1 || keepDays > 3650 {
			c.String(400, "告警历史保留天数需在 1 ~ 3650 天之间")
			return
		}

		saveConfig("alert_enabled", strconv.FormatBool(enabled))
		saveConfig("alert_offline", strconv.FormatBool(offline))
		saveConfig("alert_offline_sec", strconv.Itoa(offlineSec))
		saveConfig("alert_cooldown_min", strconv.Itoa(cooldown))
		saveConfig("alert_cpu", strconv.FormatFloat(cpu, 'f', -1, 64))
		saveConfig("alert_mem", strconv.FormatFloat(mem, 'f', -1, 64))
		saveConfig("alert_disk", strconv.FormatFloat(disk, 'f', -1, 64))
		saveConfig("alert_keep_days", strconv.Itoa(keepDays))

		globalConfig.Lock()
		globalConfig.AlertEnabled = enabled
		globalConfig.AlertOffline = offline
		globalConfig.AlertOfflineSec = offlineSec
		globalConfig.AlertCooldownMin = cooldown
		globalConfig.AlertCPU = cpu
		globalConfig.AlertMem = mem
		globalConfig.AlertDisk = disk
		globalConfig.AlertKeepDays = keepDays
		globalConfig.Unlock()

		audit(c, "alert_rules", "",
			fmt.Sprintf("告警规则：总开关=%v 离线=%v(%ds) 冷却=%dmin CPU=%.0f%% 内存=%.0f%% 磁盘=%.0f%%",
				enabled, offline, offlineSec, cooldown, cpu, mem, disk), true)
		c.Status(200)
	})

	auth.GET("/settings/alert/history", func(c *gin.Context) {
		limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
		if limit < 1 || limit > 500 {
			limit = 50
		}
		kind := cleanField(c.Query("kind"), 16)
		agentID := cleanField(c.Query("id"), 64)

		// 查询条件构造两次而不是复用同一个 *gorm.DB：
		// Count 之后继续 Find 会把 count 语句的状态带过去，
		// 拿到的是「按 kind 分组后」的行数/结果集而不是明细。
		// admin.go 的审计查询出于同样的原因也是这么写的，两处保持一致。
		build := func() *gorm.DB {
			tx := db.Model(&AlertEvent{})
			if kind != "" && kind != "all" {
				tx = tx.Where("kind = ?", kind)
			}
			if agentID != "" {
				tx = tx.Where("agent_id = ?", agentID)
			}
			return tx
		}

		var total int64
		build().Count(&total)

		rows := make([]AlertEvent, 0, limit)
		build().Order("created_at DESC").Limit(limit).Find(&rows)

		// 按类型统计，让「告警设置」页一眼看出哪类问题最多
		var byKind = make([]struct {
			Kind  string `json:"kind"`
			Count int64  `json:"count"`
		}, 0)
		db.Model(&AlertEvent{}).Select("kind, count(*) as count").Group("kind").Scan(&byKind)

		c.JSON(200, gin.H{
			"total":   total,
			"items":   rows,
			"by_kind": byKind,
		})
	})

	auth.POST("/settings/alert/history/clear", func(c *gin.Context) {
		all := c.PostForm("all") == "1"
		var deleted int64
		if all {
			deleted = db.Where("1 = 1").Delete(&AlertEvent{}).RowsAffected
		} else {
			days, _ := strconv.Atoi(c.PostForm("days"))
			if days <= 0 {
				days = 30
			}
			deleted = db.Where("created_at < ?", time.Now().AddDate(0, 0, -days)).
				Delete(&AlertEvent{}).RowsAffected
		}
		audit(c, "alert_history_clear", "", fmt.Sprintf("清理告警历史，删除 %d 条", deleted), true)
		c.JSON(200, gin.H{"deleted": deleted})
	})

	// 清空某条规则的状态，用于"我已经知道了，别再提醒"
	auth.POST("/settings/alert/ack", func(c *gin.Context) {
		id := cleanField(c.PostForm("id"), 64)
		kind := cleanField(c.PostForm("kind"), 16)
		if id == "" || kind == "" {
			c.Status(400)
			return
		}
		alertStates.Lock()
		if e := alertStates.m[alertStateKey(id, kind)]; e != nil {
			e.active = false
		}
		alertStates.Unlock()
		audit(c, "alert_ack", id, "确认告警："+kind, true)
		c.Status(200)
	})
}
