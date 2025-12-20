package main

import (
	"bytes"
	"crypto/sha256"
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
	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/disk"
	"github.com/shirou/gopsutil/v3/host"
	"github.com/shirou/gopsutil/v3/mem"
	gonet "github.com/shirou/gopsutil/v3/net"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ================= 全局配置 =================

// PingTargetConfig 定义单个 Ping 目标
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
		Token       string
		ServerURL   string
		TGToken     string
		TGChatID    string
		PingTargets []PingTargetConfig // 全局 Ping 目标列表
	}
	
	alertState = make(map[string]bool)
	DownloadBaseURL = "https://github.com/jinhuaitao/Monitor/releases/download/V1.0.0/monitor"
)

// ================= 数据库模型 =================

type User struct {
	ID       uint   `gorm:"primaryKey"`
	Username string `gorm:"unique"`
	Password string 
}

type AppConfig struct {
	Key   string `gorm:"primaryKey"`
	Value string
}

type Node struct {
	AgentID     string `gorm:"primaryKey"`
	Name        string 
	HideID      bool   `gorm:"default:false"`
	SortOrder   int    `gorm:"default:0"`
}

type PingHistory struct {
	ID        uint      `gorm:"primaryKey"`
	AgentID   string    `gorm:"index"`
	Target    string    `gorm:"index"`
	Delay     int64
	CreatedAt time.Time `gorm:"index"`
}

// ================= 传输模型 =================

type SystemStatus struct {
	AgentID         string             `json:"agent_id"`
	Name            string             `json:"name"`
	HideID          bool               `json:"hide_id"`
	SortOrder       int                `json:"sort_order"`
	PingTargets     []PingTargetConfig `json:"ping_targets"` 
	OS              string             `json:"os"`
	IP              string             `json:"ip"`
	Uptime          uint64             `json:"uptime"`
	CPUUsage        float64            `json:"cpu_usage"`
	MemUsedPercent  float64            `json:"mem_used_percent"`
	DiskUsedPercent float64            `json:"disk_used_percent"`
	NetInSpeed      uint64             `json:"net_in_speed"`
	NetOutSpeed     uint64             `json:"net_out_speed"`
	NetTotalIn      uint64             `json:"net_total_in"`  // 新增：入网总流量
	NetTotalOut     uint64             `json:"net_total_out"` // 新增：出网总流量
	PingResults     map[string]int64   `json:"ping_results"`
	LastUpdate      time.Time          `json:"last_update"`
}

type AgentResponse struct {
	Status      string             `json:"status"`
	PingTargets []PingTargetConfig `json:"ping_targets"`
}

// ================= HTML 模版 =================

const htmlDashboard = `
<!DOCTYPE html>
<html lang="zh-CN" data-theme="light">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Hub Monitor</title>
    <link href="https://fonts.googleapis.com/css2?family=Inter:wght@400;500;600;700&display=swap" rel="stylesheet">
    <script src="https://cdn.jsdelivr.net/npm/chart.js"></script>
    <style>
        :root {
            --bg-body: #f8fafc; --text-main: #1e293b; --text-sub: #64748b; 
            --primary: #4f46e5; --primary-hover: #4338ca; --danger: #ef4444; --success: #10b981; --warning: #f59e0b;
            --glass-bg: rgba(255, 255, 255, 0.7); --glass-border: rgba(255, 255, 255, 0.5); --glass-shadow: 0 8px 32px rgba(0, 0, 0, 0.05);
            --card-radius: 20px; --btn-radius: 12px;
        }
        [data-theme="dark"] {
            --bg-body: #0f172a; --text-main: #f1f5f9; --text-sub: #94a3b8;
            --glass-bg: rgba(30, 41, 59, 0.7); --glass-border: rgba(255, 255, 255, 0.08); --glass-shadow: 0 8px 32px rgba(0, 0, 0, 0.3);
            --primary: #6366f1;
        }
        * { box-sizing: border-box; transition: all 0.2s ease; }
        body { 
            font-family: 'Inter', sans-serif; background: var(--bg-body); color: var(--text-main); margin: 0; min-height: 100vh; 
            background-image: radial-gradient(at 0% 0%, rgba(99, 102, 241, 0.15) 0px, transparent 50%), radial-gradient(at 100% 100%, rgba(168, 85, 247, 0.15) 0px, transparent 50%);
            background-attachment: fixed;
        }

        /* 悬浮导航栏 */
        .header { 
            margin: 20px auto; max-width: 1240px; padding: 0 24px; height: 72px; 
            display: flex; align-items: center; justify-content: space-between; 
            background: var(--glass-bg); backdrop-filter: blur(12px); -webkit-backdrop-filter: blur(12px);
            border: 1px solid var(--glass-border); border-radius: 24px; box-shadow: var(--glass-shadow);
            position: sticky; top: 20px; z-index: 50;
        }
        .brand { font-weight: 800; font-size: 20px; color: var(--primary); display: flex; align-items: center; gap: 10px; letter-spacing: -0.5px; }
        .nav-right { display: flex; align-items: center; gap: 12px; }
        
        .btn-icon { background: rgba(128,128,128,0.1); border: none; cursor: pointer; color: var(--text-main); width: 40px; height: 40px; border-radius: 50%; display: flex; align-items: center; justify-content: center; transition: 0.2s; }
        .btn-icon:hover { background: rgba(128,128,128,0.2); transform: rotate(15deg); }
        .btn-primary { background: linear-gradient(135deg, var(--primary), #818cf8); color: white; border: none; padding: 10px 20px; border-radius: 99px; font-size: 14px; font-weight: 600; cursor: pointer; text-decoration: none; box-shadow: 0 4px 10px rgba(79, 70, 229, 0.2); }
        .btn-primary:hover { transform: translateY(-2px); box-shadow: 0 8px 20px rgba(79, 70, 229, 0.3); }
        .btn-logout { color: var(--danger); font-size: 14px; font-weight: 600; text-decoration: none; padding: 8px 16px; border-radius: 99px; background: rgba(239, 68, 68, 0.1); }
        .btn-logout:hover { background: rgba(239, 68, 68, 0.2); }

        .container { max-width: 1240px; margin: 30px auto; padding: 0 20px; }
        .card-container { display: flex; flex-direction: column; gap: 16px; }
        
        .table-header { display: grid; grid-template-columns: 100px 1.5fr 1fr 1fr 1fr 1fr 1.5fr; padding: 0 24px; font-size: 12px; font-weight: 600; color: var(--text-sub); text-transform: uppercase; margin-bottom: 10px; opacity: 0.8; }
        
        .server-row { 
            display: grid; grid-template-columns: 100px 1.5fr 1fr 1fr 1fr 1fr 1.5fr; 
            background: var(--glass-bg); backdrop-filter: blur(8px);
            border: 1px solid var(--glass-border); border-radius: 20px; 
            padding: 20px 24px; align-items: center; 
            box-shadow: 0 4px 6px -1px rgba(0, 0, 0, 0.02);
            transition: transform 0.2s, box-shadow 0.2s;
        }
        .server-row:hover { transform: translateY(-3px); box-shadow: 0 12px 24px -8px rgba(0, 0, 0, 0.08); background: rgba(255,255,255,0.9); }
        [data-theme="dark"] .server-row:hover { background: rgba(30, 41, 59, 0.9); }

        @media (max-width: 900px) {
            .header { margin: 10px; border-radius: 16px; top: 10px; }
            .table-header { display: none; }
            .server-row { grid-template-columns: 1fr; gap: 16px; padding: 20px; }
            .server-row > div { display: flex; justify-content: space-between; align-items: center; }
            .server-row > div::before { content: attr(data-label); font-weight: 600; font-size: 13px; color: var(--text-sub); margin-right: 10px; }
        }

        .status-badge { display: inline-flex; align-items: center; gap: 8px; padding: 6px 12px; border-radius: 99px; font-size: 13px; font-weight: 600; background: rgba(128,128,128,0.05); }
        .status-dot { width: 8px; height: 8px; border-radius: 50%; }
        .online { color: var(--success); background: rgba(16, 185, 129, 0.1); border: 1px solid rgba(16, 185, 129, 0.2); }
        .online .status-dot { background: var(--success); box-shadow: 0 0 8px var(--success); }
        .offline { color: var(--danger); background: rgba(239, 68, 68, 0.1); border: 1px solid rgba(239, 68, 68, 0.2); }
        .offline .status-dot { background: var(--danger); }

        .progress-group { width: 100%; max-width: 120px; }
        .progress-track { width: 100%; height: 6px; background: rgba(128,128,128,0.15); border-radius: 99px; overflow: hidden; margin-top: 6px; }
        .progress-fill { height: 100%; border-radius: 99px; transition: width 0.6s cubic-bezier(0.4, 0, 0.2, 1); }
        
        .data-meta { font-size: 12px; color: var(--text-sub); display: flex; flex-direction: column; gap: 3px; font-family: 'Menlo', monospace; }
        
        /* 移除原有的 ping-tag 样式，新增系统信息样式 */
        .sys-info { font-size: 12px; color: var(--text-sub); display: flex; flex-direction: column; gap: 4px; line-height: 1.3; }
        .sys-info strong { color: var(--text-main); font-weight: 600; }

        .name-cell { font-weight: 600; cursor: pointer; display: flex; flex-direction: column; transition:0.2s;}
        .name-cell:hover { color: var(--primary); transform: translateX(2px); }

        .modal-overlay { position: fixed; top: 0; left: 0; width: 100%; height: 100%; background: rgba(0,0,0,0.4); backdrop-filter: blur(8px); z-index: 100; display: none; align-items: center; justify-content: center; opacity: 0; transition: opacity 0.3s; }
        .modal-overlay.open { display: flex; opacity: 1; }
        .modal { background: var(--bg-body); width: 90%; max-width: 900px; border-radius: 24px; border: 1px solid var(--glass-border); display: flex; flex-direction: column; height: 700px; max-height: 85vh; overflow: hidden; transform: scale(0.95); transition: transform 0.3s cubic-bezier(0.175, 0.885, 0.32, 1.275); box-shadow: 0 25px 50px -12px rgba(0, 0, 0, 0.25); }
        .modal-overlay.open .modal { transform: scale(1); }
        .modal-header { padding: 20px 30px; border-bottom: 1px solid rgba(128,128,128,0.1); display: flex; justify-content: space-between; align-items: center; background: rgba(128,128,128,0.02); }
        .close-btn { background: none; border: none; font-size: 24px; color: var(--text-sub); cursor: pointer; transition: 0.2s; width: 32px; height: 32px; border-radius: 50%; display: flex; align-items: center; justify-content: center; }
        .close-btn:hover { background: rgba(128,128,128,0.1); color: var(--text-main); }

        .modal-body { display: flex; flex: 1; overflow: hidden; }
        .sidebar { width: 220px; background: rgba(128,128,128,0.03); border-right: 1px solid rgba(128,128,128,0.1); padding: 20px 16px; display: flex; flex-direction: column; gap: 8px; }
        .sidebar-btn { text-align: left; padding: 12px 16px; border: none; background: none; cursor: pointer; font-size: 14px; font-weight: 500; color: var(--text-sub); border-radius: 12px; transition: 0.2s; display: flex; align-items: center; gap: 12px; }
        .sidebar-btn:hover { background: rgba(128,128,128,0.08); color: var(--text-main); }
        .sidebar-btn.active { background: var(--primary); color: white; font-weight: 600; box-shadow: 0 4px 12px rgba(79, 70, 229, 0.3); }
        
        .content-area { flex: 1; padding: 30px 40px; overflow-y: auto; scroll-behavior: smooth; }
        .tab-content { display: none; animation: fadeIn 0.4s ease; }
        .tab-content.active { display: block; }
        @keyframes fadeIn { from { opacity: 0; transform: translateY(10px); } to { opacity: 1; transform: translateY(0); } }

        .form-group { margin-bottom: 24px; }
        .form-label { display: block; font-size: 13px; font-weight: 700; margin-bottom: 10px; color: var(--text-main); }
        .form-hint { font-size: 13px; color: var(--text-sub); margin-bottom: 10px; line-height: 1.6; background: rgba(128,128,128,0.05); padding: 10px; border-radius: 8px; }
        .input-text { width: 100%; padding: 12px 16px; border: 1px solid rgba(128,128,128,0.2); background: rgba(255,255,255,0.5); color: var(--text-main); border-radius: 12px; font-size: 14px; outline: none; transition: 0.2s; }
        .input-text:focus { border-color: var(--primary); background: var(--bg-body); box-shadow: 0 0 0 3px rgba(79, 70, 229, 0.1); }
        
        .btn-sm { padding: 8px 16px; font-size: 13px; border-radius: 8px; }
        .btn-del { color: var(--text-sub); border: 1px solid rgba(128,128,128,0.2); background: transparent; cursor: pointer; border-radius: 8px; padding: 8px; transition: 0.2s; }
        .btn-del:hover { color: var(--danger); border-color: var(--danger); background: rgba(239,68,68,0.05); }
        .btn-outline { background: white; border: 1px solid rgba(128,128,128,0.2); color: var(--text-main); padding: 10px 16px; border-radius: 10px; cursor: pointer; font-size: 13px; font-weight: 500; }
        .btn-outline:hover { background: rgba(128,128,128,0.05); border-color: rgba(128,128,128,0.4); }

        .cmd-box { background: #1e293b; color: #e2e8f0; padding: 20px; border-radius: 12px; font-family: 'Menlo', monospace; font-size: 13px; word-break: break-all; line-height: 1.6; border: 1px solid #334155; position: relative; box-shadow: inset 0 2px 4px rgba(0,0,0,0.2); }
        .btn-copy { position: absolute; top: 12px; right: 12px; background: rgba(255,255,255,0.1); border: 1px solid rgba(255,255,255,0.2); color: #fff; padding: 6px 12px; border-radius: 6px; font-size: 12px; cursor: pointer; transition: 0.2s; }
        .btn-copy:hover { background: rgba(255,255,255,0.2); }
        
        #pingChartContainer { height: 320px; width: 100%; margin-bottom: 20px; background: rgba(128,128,128,0.03); border-radius: 16px; padding: 10px; border:1px solid rgba(128,128,128,0.1); }
        .target-list { display: flex; flex-direction: column; gap: 10px; max-height: 350px; overflow-y: auto; margin-top: 15px; }
        .target-item { display: flex; align-items: center; gap: 12px; padding: 12px 16px; background: rgba(255,255,255,0.5); border-radius: 12px; border: 1px solid rgba(128,128,128,0.1); font-size: 14px; transition: 0.2s; }
        .target-item:hover { background: white; border-color: rgba(79, 70, 229, 0.3); transform: translateX(2px); }
        .node-row { display: flex; align-items: center; justify-content: space-between; padding: 14px 0; border-bottom: 1px solid rgba(128,128,128,0.1); gap: 12px; }
        
        .info-grid { display: grid; grid-template-columns: repeat(2, 1fr); gap: 15px; margin-bottom: 25px; }
        .info-item { background: rgba(128,128,128,0.04); padding: 15px; border-radius: 12px; border: 1px solid rgba(128,128,128,0.1); }
        .info-label { font-size: 12px; color: var(--text-sub); margin-bottom: 5px; font-weight: 600; text-transform: uppercase; }
        .info-value { font-size: 15px; font-weight: 500; font-family: 'Menlo', monospace; word-break: break-all;}
    </style>
</head>
<body>
    <div class="header">
        <div class="brand">
            <span style="background:linear-gradient(135deg, #4f46e5, #818cf8);color:white;width:32px;height:32px;border-radius:8px;display:flex;align-items:center;justify-content:center;font-size:18px;">⚡</span>
            Hub Monitor
        </div>
        <div class="nav-right">
            <button class="btn-icon" onclick="toggleTheme()" title="切换主题">
                <svg id="theme-icon" width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21 12.79A9 9 0 1 1 11.21 3 7 7 0 0 0 21 12.79z"></path></svg>
            </button>
            {{ if .IsAdmin }}
                <button class="btn-primary" onclick="openSettings()"><span>⚙️ 系统管理</span></button>
                <a href="/logout" class="btn-logout">退出</a>
            {{ else }}
                <a href="/login" class="btn-primary">管理员登录</a>
            {{ end }}
        </div>
    </div>

    <div class="container">
        <div class="table-header">
            <div>状态</div>
            <div>节点信息</div>
            <div>CPU</div>
            <div>内存</div>
            <div>硬盘</div>
            <div>网络流量</div>
            <div>系统信息</div>
        </div>
        <div id="server-list" class="card-container">
            <div style="text-align:center;padding:60px;color:var(--text-sub)">正在建立连接...</div>
        </div>
    </div>
    
    <div class="modal-overlay" id="settingsModal">
        <div class="modal">
            <div class="modal-header">
                <h3 class="modal-title" style="margin:0;font-size:18px;">系统管理</h3>
                <button class="close-btn" onclick="closeSettings()">&times;</button>
            </div>
            
            <div class="modal-body">
                <div class="sidebar">
                    <button class="sidebar-btn active" onclick="switchTab('nodes')">🖥️ 节点列表</button>
                    <button class="sidebar-btn" onclick="switchTab('targets')">🎯 监控目标</button>
                    <button class="sidebar-btn" onclick="switchTab('install')">📥 接入节点</button>
                    <button class="sidebar-btn" onclick="switchTab('alert')">🔔 告警通知</button>
                </div>

                <div class="content-area">
                    <div id="tab-nodes" class="tab-content active">
                        <h4 style="margin-top:0;margin-bottom:20px;font-size:16px;">节点管理</h4>
                        <div style="font-size:13px; color:var(--text-sub); margin-bottom:15px; padding:8px; background:rgba(79, 70, 229, 0.05); border-radius:8px; border:1px solid rgba(79, 70, 229, 0.1);">💡 提示：设置排序序号 (1, 2, 3...) 可控制节点在首页的显示顺序，数字越小越靠前。</div>
                        <div id="nodeList"></div>
                    </div>

                    <div id="tab-targets" class="tab-content">
                        <h4 style="margin-top:0;margin-bottom:20px;font-size:16px;">全局监控目标</h4>
                        <div class="form-group">
                            <div class="form-hint">支持 <b>IP</b> (ICMP Ping) 和 <b>IP:Port</b> (TCP Ping)。此设置对所有节点生效。</div>
                            <div style="display:flex;gap:10px;margin-bottom:20px;">
                                <input type="text" id="newPingTarget" class="input-text" style="flex:2" placeholder="例如: 8.8.8.8 或 192.168.1.1:80">
                                <input type="text" id="newPingAlias" class="input-text" style="flex:1" placeholder="别名 (可选)">
                                <button class="btn-primary btn-sm" onclick="addPingTarget()">+ 添加</button>
                            </div>
                            <div id="targetList" class="target-list"></div>
                        </div>
                    </div>

                    <div id="tab-install" class="tab-content">
                        <h4 style="margin-top:0;margin-bottom:20px;font-size:16px;">接入新节点</h4>
                        <div class="form-group">
                            <label class="form-label">1. 面板公网地址 (Server URL)</label>
                            <div style="display:flex;gap:10px;">
                                <input type="text" id="serverUrlInput" class="input-text" placeholder="http://YOUR_IP:PORT">
                                <button class="btn-outline" onclick="saveServerUrl()">保存</button>
                            </div>
                        </div>
                        <div class="form-group">
                            <label class="form-label">2. 通信 Token</label>
                            <div style="display:flex;gap:10px;">
                                <input type="text" id="tokenInput" class="input-text">
                                <button class="btn-outline" onclick="saveToken()">更新</button>
                            </div>
                        </div>
                        <div class="form-group">
                            <label class="form-label">3. 一键安装命令</label>
                            <div class="cmd-box">
                                <span id="installCmd"></span>
                                <button class="btn-copy" onclick="copyCmd()">复制</button>
                            </div>
                            <div style="margin-top:10px;font-size:13px;color:var(--text-sub);line-height:1.5">
                                适用于 Linux (amd64/arm64)。脚本会自动下载二进制文件，配置 Systemd (Debian/CentOS) 或 OpenRC (Alpine) 服务并启动。
                            </div>
                        </div>
                    </div>

                    <div id="tab-alert" class="tab-content">
                        <h4 style="margin-top:0;margin-bottom:20px;font-size:16px;">Telegram 告警</h4>
                        <div class="form-group">
                            <label class="form-label">Bot Token</label>
                            <input type="text" id="tgToken" class="input-text" placeholder="123456:ABC-DEF...">
                        </div>
                        <div class="form-group">
                            <label class="form-label">Chat ID</label>
                            <div style="display:flex;gap:10px;">
                                <input type="text" id="tgChat" class="input-text" placeholder="-100123456789">
                                <button class="btn-outline" onclick="saveAlert()">保存配置</button>
                                <button class="btn-outline" onclick="testAlert()">测试</button>
                            </div>
                        </div>
                    </div>
                </div>
            </div>
        </div>
    </div>
    
    <div class="modal-overlay" id="detailModal">
        <div class="modal">
            <div class="modal-header">
                <h3 class="modal-title" style="margin:0;font-size:18px;">系统信息</h3>
                <button class="close-btn" onclick="closeDetailModal()">&times;</button>
            </div>
            <div class="content-area" style="display:block; padding:30px;">
                <div id="nodeInfoGrid" class="info-grid">
                    </div>
                
                <h4 style="margin-top:20px;margin-bottom:15px;font-size:15px;color:var(--text-main);">网络延迟监控</h4>
                <div id="pingChartContainer"><canvas id="pingChart"></canvas></div>
                <div style="text-align:center;color:var(--text-sub);font-size:13px;margin-top:15px;">
                    如需修改监控目标，请前往 <b style="color:var(--text-main)">系统管理</b> -> <b style="color:var(--text-main)">监控目标</b>
                </div>
            </div>
        </div>
    </div>

<script>
    function initTheme() {
        var t = localStorage.getItem('theme') || (window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light');
        document.documentElement.setAttribute('data-theme', t);
        updateIcon(t);
    }
    function toggleTheme() {
        var t = document.documentElement.getAttribute('data-theme') === 'dark' ? 'light' : 'dark';
        document.documentElement.setAttribute('data-theme', t);
        localStorage.setItem('theme', t);
        updateIcon(t);
        if(pingChart) pingChart.update();
    }
    function updateIcon(t) {
        var sun = '<circle cx="12" cy="12" r="5"></circle><line x1="12" y1="1" x2="12" y2="3"></line><line x1="12" y1="21" x2="12" y2="23"></line><line x1="4.22" y1="4.22" x2="5.64" y2="5.64"></line><line x1="18.36" y1="18.36" x2="19.78" y2="19.78"></line><line x1="1" y1="12" x2="3" y2="12"></line><line x1="21" y1="12" x2="23" y2="12"></line><line x1="4.22" y1="19.78" x2="5.64" y2="18.36"></line><line x1="18.36" y1="5.64" x2="19.78" y2="4.22"></line>';
        var moon = '<path d="M21 12.79A9 9 0 1 1 11.21 3 7 7 0 0 0 21 12.79z"></path>';
        document.getElementById('theme-icon').innerHTML = t==='dark' ? sun : moon;
    }
    initTheme();

    var currentToken = "{{ .Token }}";
    var customUrl = "{{ .CustomServerURL }}";
    var browserUrl = "{{ .BrowserURL }}";
    var downloadUrl = "{{ .DownloadURL }}";
    var isAdmin = {{ .IsAdmin }};
    var tgToken = "{{ .TGToken }}";
    var tgChat = "{{ .TGChatID }}";
    var pingChart = null;
    var currentTargets = []; 
    var currentStatsData = {}; // 缓存当前节点数据

    function updateCmdDisplay() {
        if (!isAdmin) return;
        var sUrl = customUrl ? customUrl : browserUrl;
        var cmd = 'curl -L -o monitor ' + downloadUrl + ' && chmod +x monitor && ./monitor -mode install -server ' + sUrl + ' -token ' + currentToken + ' -id $(hostname)';
        var el = document.getElementById('installCmd'); if(el) el.innerText = cmd;
        var tEl = document.getElementById('tokenInput'); if(tEl) tEl.value = currentToken;
        var uEl = document.getElementById('serverUrlInput'); if(uEl) uEl.value = customUrl;
        var tgEl = document.getElementById('tgToken'); if(tgEl) tgEl.value = tgToken;
        var tcEl = document.getElementById('tgChat'); if(tcEl) tcEl.value = tgChat;
    }
    
    function copyCmd() {
        var range = document.createRange();
        range.selectNode(document.getElementById("installCmd"));
        window.getSelection().removeAllRanges();
        window.getSelection().addRange(range);
        document.execCommand("copy");
        window.getSelection().removeAllRanges();
        var btn = document.querySelector('.btn-copy');
        btn.innerText = "已复制"; setTimeout(function(){ btn.innerText = "复制"; }, 1500);
    }

    function formatBytes(b) {
        if(b===0)return'0 B'; var i=Math.floor(Math.log(b)/Math.log(1024));
        return parseFloat((b/Math.pow(1024,i)).toFixed(1))+' '+['B','KB','MB','GB'][i];
    }
    function formatSpeed(b) { return formatBytes(b)+'/s'; }
    function formatUptime(s) { return Math.floor(s/86400)+'天'; }

    // 主 Dashboard 更新
    function updateStats() {
        fetch('/api/stats').then(function(r){return r.json()}).then(function(data) {
            currentStatsData = data; // 更新缓存
            var tbody = document.getElementById('server-list');
            
            var ids = Object.keys(data).sort(function(a,b){
                var sa = data[a].sort_order || 0;
                var sb = data[b].sort_order || 0;
                if (sa !== sb) return sa - sb;
                var na = data[a].name || data[a].agent_id;
                var nb = data[b].name || data[b].agent_id;
                return na.localeCompare(nb);
            });

            if(ids.length===0){tbody.innerHTML='<div style="text-align:center;padding:40px;color:var(--text-sub)">暂无活跃节点，请点击“接入节点”获取安装命令。</div>';return;}
            
            if(ids.length > 0 && data[ids[0]].ping_targets) {
                currentTargets = data[ids[0]].ping_targets; 
            }

            var html = '';
            ids.forEach(function(id) {
                var s = data[id];
                var online = (new Date()-new Date(s.last_update))/1000 < 25;
                var displayName = s.hide_id ? (s.name||s.agent_id) : (s.name ? s.name+'<br><span style="font-size:12px;color:var(--text-sub);font-weight:400">'+s.agent_id+'</span>' : s.agent_id);
                var cColor = s.cpu_usage>80?'var(--danger)':'var(--primary)';
                var mColor = s.mem_used_percent>85?'var(--danger)':'var(--success)';
                
                // 计算总流量显示
                var totalTraffic = formatBytes(s.net_total_in + s.net_total_out);

                html += '<div class="server-row">' +
                    '<div data-label="状态"><div class="status-badge ' + (online?'online':'offline') + '"><span class="status-dot"></span>' + (online?'运行中':'离线') + '</div></div>' +
                    '<div data-label="节点"><div class="name-cell" onclick="openNodeDetails(\'' + id + '\')">' + displayName + '</div></div>' +
                    '<div data-label="CPU"><div class="progress-group"><div style="font-size:12px;display:flex;justify-content:space-between;margin-bottom:2px"><span>' + s.cpu_usage.toFixed(0) + '%</span></div><div class="progress-track"><div class="progress-fill" style="width:' + s.cpu_usage + '%;background:' + cColor + '"></div></div></div></div>' +
                    '<div data-label="内存"><div class="progress-group"><div style="font-size:12px;display:flex;justify-content:space-between;margin-bottom:2px"><span>' + s.mem_used_percent.toFixed(0) + '%</span></div><div class="progress-track"><div class="progress-fill" style="width:' + s.mem_used_percent + '%;background:' + mColor + '"></div></div></div></div>' +
                    '<div data-label="硬盘" style="font-family:monospace;font-size:13px">' + s.disk_used_percent.toFixed(0) + '%</div>' +
                    '<div data-label="网络"><div class="data-meta">' +
                        '<div>↓ ' + formatSpeed(s.net_in_speed) + '</div>' +
                        '<div>↑ ' + formatSpeed(s.net_out_speed) + '</div>' +
                    '</div></div>' +
                    '<div data-label="系统信息"><div class="sys-info">' +
                        '<div><strong>' + s.os + '</strong></div>' +
                        '<div>运行: ' + formatUptime(s.uptime) + '</div>' +
                        '<div>流量: ' + totalTraffic + '</div>' +
                    '</div></div>' +
                '</div>';
            });
            tbody.innerHTML = html;
        });
    }
    setInterval(updateStats, 2000); updateStats(); updateCmdDisplay();

    var modal = document.getElementById('settingsModal');
    var detailModal = document.getElementById('detailModal');
    
    function openSettings() { if(modal) modal.classList.add('open'); loadNodeList(); loadGlobalTargets(); }
    function closeSettings() { if(modal) modal.classList.remove('open'); }
    function closeDetailModal() { if(detailModal) detailModal.classList.remove('open'); }
    
    function switchTab(t) {
        var contents = document.querySelectorAll('.tab-content');
        for(var i=0; i<contents.length; i++) contents[i].classList.remove('active');
        var btns = document.querySelectorAll('.sidebar-btn'); 
        for(var i=0; i<btns.length; i++) btns[i].classList.remove('active');
        document.getElementById('tab-'+t).classList.add('active');
        event.target.classList.add('active');
    }

    function saveServerUrl() {
        var u = document.getElementById('serverUrlInput').value;
        fetch('/api/settings/url', {method:'POST',headers:{'Content-Type':'application/x-www-form-urlencoded'},body:'url='+encodeURIComponent(u)})
        .then(function(){ customUrl=u; updateCmdDisplay(); alert('已保存'); });
    }
    function saveToken() {
        var t = document.getElementById('tokenInput').value;
        if(confirm('修改Token需重启Agent，确定？')) fetch('/api/settings/token', {method:'POST',headers:{'Content-Type':'application/x-www-form-urlencoded'},body:'token='+encodeURIComponent(t)}).then(function(){alert('已保存');});
    }
    function loadNodeList() {
        if(!isAdmin) return;
        fetch('/api/stats').then(function(r){return r.json()}).then(function(data) {
            var el = document.getElementById('nodeList');
            var ids = Object.keys(data).sort();
            var html = '';
            if(ids.length===0) html='<div style="text-align:center;padding:20px;color:var(--text-sub)">暂无接入节点</div>';
            else ids.forEach(function(id) {
                var s=data[id];
                var op=s.hide_id?0.5:1;
                var sort = s.sort_order || 0;
                html+='<div class="node-row"><div style="flex:1;font-weight:600">'+(s.name||s.agent_id)+'<div style="font-size:12px;color:var(--text-sub);font-weight:400">'+s.agent_id+'</div></div>' +
                '<div style="display:flex;gap:8px"><button class="btn-outline" style="opacity:'+op+'" onclick="toggleHide(\''+id+'\')">👁️</button>' +
                '<input type="number" class="input-text" style="width:60px;padding:8px;text-align:center" value="'+sort+'" placeholder="排序" id="s-'+id+'">' +
                '<input type="text" class="input-text" style="width:120px;padding:8px" value="'+(s.name||'')+'" placeholder="设置别名" id="n-'+id+'">' +
                '<button class="btn-outline" onclick="saveNode(\''+id+'\')">保存</button><button class="btn-del" onclick="deleteNode(\''+id+'\')">🗑️</button></div></div>';
            });
            el.innerHTML = html;
        });
    }
    function saveNode(id) { 
        var n = document.getElementById('n-'+id).value;
        var s = document.getElementById('s-'+id).value;
        fetch('/api/settings/update_node', {method:'POST',headers:{'Content-Type':'application/x-www-form-urlencoded'},body:'id='+id+'&name='+encodeURIComponent(n)+'&sort='+s}).then(function(){loadNodeList(); updateStats();}); 
    }
    function toggleHide(id) { fetch('/api/settings/toggle_hide', {method:'POST',headers:{'Content-Type':'application/x-www-form-urlencoded'},body:'id='+id}).then(function(){loadNodeList()}); }
    function deleteNode(id) { if(confirm('确认删除?')) fetch('/api/settings/delete', {method:'POST',headers:{'Content-Type':'application/x-www-form-urlencoded'},body:'id='+id}).then(function(){loadNodeList()}); }
    
    function saveAlert() {
        var t = document.getElementById('tgToken').value;
        var c = document.getElementById('tgChat').value;
        fetch('/api/settings/alert', {method:'POST',headers:{'Content-Type':'application/x-www-form-urlencoded'},body:'token='+encodeURIComponent(t)+'&chat='+encodeURIComponent(c)}).then(function(){alert('告警配置已保存');});
    }
    function testAlert() { fetch('/api/settings/test_alert', {method:'POST'}).then(function(){alert('测试消息已发送');}); }
    
    // ============= 全局 Ping 管理 =============
    function loadGlobalTargets() {
        if(!isAdmin) return;
        fetch('/api/settings/get_global_targets').then(function(r){return r.json()}).then(function(data){
            currentTargets = data || [];
            renderTargets();
        });
    }

    function renderTargets() {
        var html = '';
        if(currentTargets.length === 0) html = '<div style="text-align:center;color:var(--text-sub);padding:30px;background:rgba(128,128,128,0.02);border-radius:12px;">暂无监控目标</div>';
        currentTargets.forEach(function(t, idx){
            var display = t.alias ? (t.alias + ' <span style="color:var(--text-sub);font-size:12px;margin-left:5px">(' + t.target + ')</span>') : t.target;
            html += '<div class="target-item"><div style="flex:1;">' + display + '</div><button class="btn-del" onclick="removeTarget(' + idx + ')">&times;</button></div>';
        });
        document.getElementById('targetList').innerHTML = html;
    }

    function addPingTarget() {
        var val = document.getElementById('newPingTarget').value;
        var alias = document.getElementById('newPingAlias').value;
        if(val) {
            currentTargets.push({target: val, alias: alias});
            saveGlobalTargets();
            document.getElementById('newPingTarget').value = '';
            document.getElementById('newPingAlias').value = '';
        }
    }

    function removeTarget(idx) {
        currentTargets.splice(idx, 1);
        saveGlobalTargets();
    }

    function saveGlobalTargets() {
        fetch('/api/settings/save_global_targets', {
            method:'POST',
            headers:{'Content-Type':'application/json'},
            body:JSON.stringify(currentTargets)
        }).then(function(){ renderTargets(); });
    }

    // ============= 详情弹窗逻辑 =============
    function openNodeDetails(id) {
        if(!currentStatsData[id]) return;
        var s = currentStatsData[id];
        
        // 填充静态信息
        var infoHtml = 
            '<div class="info-item"><div class="info-label">节点名称 / ID</div><div class="info-value">' + (s.name||s.agent_id) + '<br><span style="font-size:12px;color:var(--text-sub)">' + s.agent_id + '</span></div></div>' +
            '<div class="info-item"><div class="info-label">操作系统</div><div class="info-value">' + s.os + '</div></div>' +
            '<div class="info-item"><div class="info-label">IP 地址</div><div class="info-value">' + s.ip + '</div></div>' +
            '<div class="info-item"><div class="info-label">持续运行</div><div class="info-value">' + formatUptime(s.uptime) + '</div></div>';
            
        document.getElementById('nodeInfoGrid').innerHTML = infoHtml;
        
        detailModal.classList.add('open');
        loadPingChart(id);
    }
    
    function loadPingChart(id) {
        var aliasMap = {};
        if(currentTargets) {
            currentTargets.forEach(function(t){ aliasMap[t.target] = t.alias; });
        }

        fetch('/api/history/ping?id=' + id).then(function(r){return r.json()}).then(function(data) {
            var ctx = document.getElementById('pingChart').getContext('2d');
            if(pingChart) pingChart.destroy();
            
            var datasets = {};
            var times = new Set();
            
            data.forEach(function(d){
                var tStr = new Date(d.time).toLocaleTimeString();
                times.add(tStr);
                if(!datasets[d.target]) datasets[d.target] = [];
                datasets[d.target].push({x: tStr, y: d.delay});
            });

            var sortedTimes = Array.from(times).sort();
            var chartData = [];
            var colors = ['#4f46e5', '#10b981', '#f59e0b', '#ef4444', '#8b5cf6', '#ec4899'];
            var i = 0;

            Object.keys(datasets).forEach(function(target){
                var label = aliasMap[target] || target;
                chartData.push({
                    label: label,
                    data: datasets[target],
                    borderColor: colors[i % colors.length],
                    backgroundColor: 'transparent',
                    borderWidth: 2,
                    tension: 0.3,
                    pointRadius: 0
                });
                i++;
            });

            var isDark = document.documentElement.getAttribute('data-theme') === 'dark';
            var gridColor = isDark ? '#334155' : '#e5e7eb';
            var textColor = isDark ? '#94a3b8' : '#6b7280';

            pingChart = new Chart(ctx, {
                type: 'line',
                data: { labels: sortedTimes, datasets: chartData },
                options: {
                    responsive: true,
                    maintainAspectRatio: false,
                    interaction: { intersect: false, mode: 'index' },
                    plugins: { legend: { display: true, labels: { color: textColor } } },
                    scales: {
                        x: { grid: { display: false, borderColor: gridColor }, ticks: { color: textColor, maxTicksLimit: 6 } },
                        y: { grid: { color: gridColor, borderColor: gridColor }, ticks: { color: textColor }, beginAtZero: true }
                    }
                }
            });
        });
    }
</script>
</body>
</html>
`

const htmlLogin = `
<!DOCTYPE html>
<html lang="zh-CN">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>登录 - Hub Monitor</title>
    <link href="https://fonts.googleapis.com/css2?family=Inter:wght@400;500;600&display=swap" rel="stylesheet">
    <style>
        :root { --primary: #4f46e5; --primary-hover: #4338ca; --bg: #f3f4f6; --text: #1f2937; }
        body { margin: 0; font-family: 'Inter', sans-serif; background: #eef2f6; display: flex; align-items: center; justify-content: center; height: 100vh; overflow: hidden; position: relative; }
        
        body::before, body::after { content: ''; position: absolute; border-radius: 50%; filter: blur(80px); z-index: -1; opacity: 0.6; }
        body::before { width: 300px; height: 300px; background: #818cf8; top: -50px; left: -50px; animation: float 10s infinite alternate; }
        body::after { width: 400px; height: 400px; background: #c084fc; bottom: -100px; right: -100px; animation: float 12s infinite alternate-reverse; }
        @keyframes float { from { transform: translate(0, 0); } to { transform: translate(30px, 50px); } }

        .login-card { background: rgba(255, 255, 255, 0.85); backdrop-filter: blur(20px); -webkit-backdrop-filter: blur(20px); padding: 48px 40px; border-radius: 24px; box-shadow: 0 20px 40px -10px rgba(0,0,0,0.1); width: 100%; max-width: 360px; border: 1px solid rgba(255,255,255,0.5); transform: translateY(0); transition: 0.3s; }
        .login-card:hover { transform: translateY(-5px); box-shadow: 0 25px 50px -12px rgba(0,0,0,0.15); }
        
        .brand { text-align: center; margin-bottom: 32px; }
        .brand-icon { width: 48px; height: 48px; background: var(--primary); color: white; border-radius: 12px; display: inline-flex; align-items: center; justify-content: center; font-size: 24px; margin-bottom: 16px; box-shadow: 0 10px 15px -3px rgba(79, 70, 229, 0.3); }
        .brand h2 { margin: 0; font-size: 24px; font-weight: 700; color: var(--text); letter-spacing: -0.5px; }
        .brand p { margin: 8px 0 0; color: #6b7280; font-size: 14px; }

        .form-group { margin-bottom: 20px; position: relative; }
        .input-field { width: 100%; padding: 14px 16px; background: rgba(255,255,255,0.9); border: 2px solid #e5e7eb; border-radius: 12px; font-size: 15px; outline: none; transition: 0.2s; color: var(--text); box-sizing: border-box; }
        .input-field:focus { border-color: var(--primary); box-shadow: 0 0 0 4px rgba(79, 70, 229, 0.1); background: white; }
        .input-field::placeholder { color: #9ca3af; }

        .btn-submit { width: 100%; padding: 14px; background: linear-gradient(135deg, var(--primary), #6366f1); color: white; border: none; border-radius: 12px; font-size: 16px; font-weight: 600; cursor: pointer; transition: 0.3s; box-shadow: 0 4px 6px -1px rgba(79, 70, 229, 0.2); margin-top: 10px; }
        .btn-submit:hover { transform: translateY(-1px); box-shadow: 0 10px 15px -3px rgba(79, 70, 229, 0.3); filter: brightness(1.1); }
        .btn-submit:active { transform: translateY(1px); }

        .footer { text-align: center; margin-top: 24px; font-size: 13px; color: #9ca3af; }
    </style>
</head>
<body>
    <div class="login-card">
        <div class="brand">
            <div class="brand-icon">⚡</div>
            <h2>Hub Monitor</h2>
            <p>请登录以管理您的节点</p>
        </div>
        <form method="POST" action="{{ .Action }}">
            <div class="form-group">
                <input type="text" name="username" class="input-field" placeholder="用户名" required autocomplete="off">
            </div>
            <div class="form-group">
                <input type="password" name="password" class="input-field" placeholder="密码" required>
            </div>
            <button type="submit" class="btn-submit">登 录</button>
        </form>
        <div class="footer">&copy; 2024 Monitor System</div>
    </div>
</body>
</html>
`

// ================= 主程序 =================

func main() {
	mode := flag.String("mode", "server", "Mode")
	port := flag.String("port", "8080", "Port")
	sAddr := flag.String("server", "http://localhost:8080", "Agent Server")
	tkn := flag.String("token", "", "Agent Token")
	aid := flag.String("id", "", "Agent ID")

	flag.Parse()

	if *mode == "agent" {
		if *tkn == "" || *aid == "" { panic("Agent need -token and -id") }
		runAgent(*sAddr, *tkn, *aid)
	} else if *mode == "install" {
		installAgent(*sAddr, *tkn, *aid)
	} else {
		runServer(*port)
	}
}

// ================= 安装逻辑 =================

func installAgent(server, token, id string) {
	fmt.Println(">> 正在安装监控 Agent...")
	binPath, err := filepath.Abs(os.Args[0])
	if err != nil { fmt.Println("错误: 无法获取文件路径"); return }
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

// ================= 服务端 =================

func runServer(port string) {
	var err error
	db, err = gorm.Open(sqlite.Open("monitor.db"), &gorm.Config{})
	if err != nil { panic(err) }
	
	db.AutoMigrate(&User{}, &AppConfig{}, &Node{}, &PingHistory{})
	loadGlobalConfig()
	go monitorAlerts()
	go cleanupHistory()

	gin.SetMode(gin.ReleaseMode)
	r := gin.Default()
	
	store := cookie.NewStore([]byte("12345678901234567890123456789012"))
	store.Options(sessions.Options{Path: "/", MaxAge: 3600 * 24})
	r.Use(sessions.Sessions("mysession", store))

	r.GET("/", func(c *gin.Context) {
		var cnt int64; db.Model(&User{}).Count(&cnt)
		if cnt == 0 { c.Redirect(302, "/setup"); return }
		dashboardHandler(c)
	})
	
	r.GET("/setup", func(c *gin.Context) {
		var cnt int64; db.Model(&User{}).Count(&cnt)
		if cnt>0{c.Redirect(302,"/login");return}
		t,_:=template.New("s").Parse(htmlLogin); t.Execute(c.Writer, map[string]interface{}{"Action":"/setup"})
	})
	r.POST("/setup", func(c *gin.Context) {
		u,p:=c.PostForm("username"),c.PostForm("password")
		if u!=""&&p!=""{ db.Create(&User{Username:u,Password:hashPwd(p)}); c.Redirect(302,"/login") }
	})
	r.GET("/login", func(c *gin.Context) {
		var cnt int64; db.Model(&User{}).Count(&cnt)
		if cnt == 0 { c.Redirect(302, "/setup"); return }
		if sessions.Default(c).Get("user")!=nil{c.Redirect(302,"/");return}
		t,_:=template.New("l").Parse(htmlLogin); t.Execute(c.Writer, map[string]interface{}{"Action":"/login"})
	})
	r.POST("/login", func(c *gin.Context) {
		u,p:=c.PostForm("username"),c.PostForm("password")
		var user User
		if db.Where("username=? AND password=?",u,hashPwd(p)).First(&user).Error==nil {
			s:=sessions.Default(c); s.Set("user",u); s.Save(); c.Redirect(302,"/")
		} else { c.Redirect(302,"/login") }
	})
	r.GET("/logout", func(c *gin.Context) { s:=sessions.Default(c);s.Clear();s.Save();c.Redirect(302,"/") })

	api := r.Group("/api")
	{
		api.POST("/report", func(c *gin.Context) {
			globalConfig.RLock(); t:=globalConfig.Token; targets:=globalConfig.PingTargets; globalConfig.RUnlock()
			if c.Query("token")!=t { c.AbortWithStatus(401); return }
			
			var s SystemStatus
			if err:=c.ShouldBindJSON(&s); err==nil {
				s.LastUpdate = time.Now()
				if s.IP == "" { s.IP = c.ClientIP() }
				
				var node Node
				db.Clauses(clause.OnConflict{DoNothing:true}).Create(&Node{AgentID:s.AgentID})
				db.First(&node, "agent_id = ?", s.AgentID)
				
				// 记录多目标 Ping 历史
				for target, delay := range s.PingResults {
					if delay > 0 {
						db.Create(&PingHistory{AgentID: s.AgentID, Target: target, Delay: delay, CreatedAt: time.Now()})
					}
				}

				s.Name = node.Name
				s.HideID = node.HideID
				s.SortOrder = node.SortOrder
				s.PingTargets = targets // 下发全局配置
				
				cacheMutex.Lock(); statusCache[s.AgentID]=s; cacheMutex.Unlock()
				c.JSON(200, AgentResponse{Status: "ok", PingTargets: targets})
			}
		})

		// 开放 API：获取状态列表（去敏）
		api.GET("/stats", func(c *gin.Context) {
			isAdmin := sessions.Default(c).Get("user") != nil
			cacheMutex.RLock(); defer cacheMutex.RUnlock()
			res := make(map[string]SystemStatus)
			for k,v := range statusCache {
				if !isAdmin { v.IP = "Hidden" }
				res[k]=v
			}
			c.JSON(200, res)
		})
		
		// 开放 API：获取 Ping 历史图表
		api.GET("/history/ping", func(c *gin.Context) {
			id := c.Query("id")
			var history []PingHistory
			// 获取最近 100 条记录
			db.Where("agent_id = ?", id).Order("created_at desc").Limit(100).Find(&history)
			var res []gin.H
			for i := len(history)-1; i >= 0; i-- {
				res = append(res, gin.H{"time": history[i].CreatedAt, "delay": history[i].Delay, "target": history[i].Target})
			}
			c.JSON(200, res)
		})

		// 以下 API 需要登录
		auth := api.Group("/")
		auth.Use(authMiddleware())
		{
			auth.POST("/settings/token", func(c *gin.Context) {
				t:=c.PostForm("token"); if len(t)<3{c.Status(400);return}
				saveConfig("token", t); globalConfig.Lock(); globalConfig.Token=t; globalConfig.Unlock(); c.Status(200)
			})
			auth.POST("/settings/url", func(c *gin.Context) {
				u:=c.PostForm("url"); saveConfig("server_url", u); globalConfig.Lock(); globalConfig.ServerURL=u; globalConfig.Unlock(); c.Status(200)
			})
			auth.POST("/settings/alert", func(c *gin.Context) {
				tk, ch := c.PostForm("token"), c.PostForm("chat")
				saveConfig("tg_token", tk); saveConfig("tg_chat", ch)
				globalConfig.Lock(); globalConfig.TGToken=tk; globalConfig.TGChatID=ch; globalConfig.Unlock()
				c.Status(200)
			})
			auth.POST("/settings/test_alert", func(c *gin.Context) {
				sendTelegram("🔔 测试告警消息\nMonitor 配置成功！")
				c.Status(200)
			})
			auth.POST("/settings/update_node", func(c *gin.Context) {
				id := c.PostForm("id")
				name := c.PostForm("name")
				sort, _ := strconv.Atoi(c.PostForm("sort"))
				db.Model(&Node{}).Where("agent_id=?",id).Updates(map[string]interface{}{"name":name, "sort_order":sort})
				c.Status(200)
			})
			
			// 全局 Ping 目标管理 API (替换原来的 get_targets / save_targets)
			auth.GET("/settings/get_global_targets", func(c *gin.Context) {
				globalConfig.RLock(); defer globalConfig.RUnlock()
				c.JSON(200, globalConfig.PingTargets)
			})
			
			auth.POST("/settings/save_global_targets", func(c *gin.Context) {
				var targets []PingTargetConfig
				if c.ShouldBindJSON(&targets) == nil {
					b, _ := json.Marshal(targets)
					saveConfig("sys_ping_targets", string(b))
					globalConfig.Lock(); globalConfig.PingTargets = targets; globalConfig.Unlock()
					c.Status(200)
				}
			})
			
			auth.POST("/settings/toggle_hide", func(c *gin.Context) {
				var n Node; db.First(&n,"agent_id=?",c.PostForm("id")); db.Model(&n).Update("hide_id", !n.HideID); c.Status(200)
			})
			auth.POST("/settings/delete", func(c *gin.Context) {
				id:=c.PostForm("id"); db.Delete(&Node{},"agent_id=?",id); cacheMutex.Lock(); delete(statusCache,id); cacheMutex.Unlock(); c.Status(200)
			})
		}
	}

	fmt.Printf(">> http://localhost:%s\n", port)
	r.Run(":" + port)
}

func monitorAlerts() {
	for {
		time.Sleep(10 * time.Second)
		cacheMutex.RLock()
		for id, s := range statusCache {
			isOffline := time.Since(s.LastUpdate) > 30*time.Second
			alreadyAlerted := alertState[id]
			if isOffline && !alreadyAlerted {
				alertState[id] = true
				sendTelegram(fmt.Sprintf("🔴 节点离线告警\nID: %s\nName: %s\nIP: %s", s.AgentID, s.Name, s.IP))
			} else if !isOffline && alreadyAlerted {
				alertState[id] = false
				sendTelegram(fmt.Sprintf("🟢 节点恢复上线\nID: %s\nName: %s", s.AgentID, s.Name))
			}
		}
		cacheMutex.RUnlock()
	}
}

func cleanupHistory() {
	for {
		time.Sleep(1 * time.Hour)
		db.Where("created_at < ?", time.Now().Add(-24*time.Hour)).Delete(&PingHistory{})
	}
}

func sendTelegram(msg string) {
	globalConfig.RLock(); token:=globalConfig.TGToken; chat:=globalConfig.TGChatID; globalConfig.RUnlock()
	if token == "" || chat == "" { return }
	http.PostForm(fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", token), map[string][]string{"chat_id": {chat}, "text": {msg}})
}

func loadGlobalConfig() {
	var cfgs []AppConfig; db.Find(&cfgs)
	globalConfig.Token = "default-token"
	
	// 默认 Ping 目标
	defaultTargets := []PingTargetConfig{{Target: "8.8.8.8:53", Alias: "Google DNS"}}

	for _, c := range cfgs {
		switch c.Key {
		case "token": globalConfig.Token = c.Value
		case "server_url": globalConfig.ServerURL = c.Value
		case "tg_token": globalConfig.TGToken = c.Value
		case "tg_chat": globalConfig.TGChatID = c.Value
		case "sys_ping_targets": 
			json.Unmarshal([]byte(c.Value), &globalConfig.PingTargets)
		}
	}
	
	if len(globalConfig.PingTargets) == 0 {
		globalConfig.PingTargets = defaultTargets
	}
	
	if len(cfgs) == 0 { saveConfig("token", "default-token") }
}

func saveConfig(k, v string) { db.Clauses(clause.OnConflict{UpdateAll:true}).Create(&AppConfig{Key:k, Value:v}) }

func dashboardHandler(c *gin.Context) {
	s := sessions.Default(c); isAdmin := s.Get("user") != nil
	t,_:=template.New("d").Parse(htmlDashboard)
	sch:="http://"; if c.Request.TLS!=nil{sch="https://"}
	
	globalConfig.RLock(); tk:=globalConfig.Token; u:=globalConfig.ServerURL; tgt:=globalConfig.TGToken; tgc:=globalConfig.TGChatID; globalConfig.RUnlock()
	if !isAdmin { tk=""; u=""; tgt=""; tgc="" }

	t.Execute(c.Writer, map[string]interface{}{
		"BrowserURL": sch+c.Request.Host, "CustomServerURL": u, "Token": tk, "TGToken": tgt, "TGChatID": tgc,
		"DownloadURL": DownloadBaseURL, "IsAdmin": isAdmin,
	})
}

func authMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		var cnt int64; db.Model(&User{}).Count(&cnt)
		if cnt==0{c.Redirect(302,"/setup");c.Abort();return}
		if sessions.Default(c).Get("user")==nil{c.Redirect(302,"/login");c.Abort();return}
		c.Next()
	}
}

func hashPwd(s string) string { h:=sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

// ================= Agent =================

func runAgent(server, token, id string) {
	fmt.Printf("Agent -> %s (ID:%s)\n", server, id)
	url := fmt.Sprintf("%s/api/report?token=%s", server, token)
	client := &http.Client{Timeout: 5 * time.Second}
	
	hostInfo, _ := host.Info()
	osInfo := fmt.Sprintf("%s %s", hostInfo.Platform, hostInfo.PlatformVersion)

	var lastIn, lastOut uint64; var lastTime time.Time
	
	// 默认 Ping 目标列表
	currentTargets := []PingTargetConfig{{Target: "8.8.8.8:53"}}

	for {
		cIdx, _ := cpu.Percent(0, false)
		vm, _ := mem.VirtualMemory()
		du, _ := disk.Usage("/")
		nio, _ := gonet.IOCounters(false)
		
		cVal := 0.0; if len(cIdx)>0 { cVal=cIdx[0] }
		curIn, curOut := uint64(0), uint64(0); if len(nio)>0 { curIn=nio[0].BytesRecv; curOut=nio[0].BytesSent }
		
		now := time.Now()
		spIn, spOut := uint64(0), uint64(0)
		if !lastTime.IsZero() {
			d := now.Sub(lastTime).Seconds()
			if d>0 {
				if curIn>=lastIn { spIn=uint64(float64(curIn-lastIn)/d) }
				if curOut>=lastOut { spOut=uint64(float64(curOut-lastOut)/d) }
			}
		}
		lastIn,lastOut,lastTime = curIn,curOut,now

		// 多目标 Ping (混合 TCP 和 ICMP)
		pingResults := make(map[string]int64)
		var wg sync.WaitGroup
		var mu sync.Mutex
		
		for _, t := range currentTargets {
			wg.Add(1)
			go func(target string) {
				defer wg.Done()
				var ms int64
				// 判断是否包含端口 (TCP Ping vs ICMP Ping)
				if strings.Contains(target, ":") {
					start := time.Now()
					if conn, err := net.DialTimeout("tcp", target, 2*time.Second); err == nil {
						ms = time.Since(start).Milliseconds()
						conn.Close()
					}
				} else {
					// 无端口则使用系统 Ping (ICMP)
					ms = execPing(target)
				}
				if ms > 0 {
					mu.Lock(); pingResults[target] = ms; mu.Unlock()
				}
			}(t.Target)
		}
		wg.Wait()

		uptime, _ := host.Uptime()

		s := SystemStatus{
			AgentID: id, OS: osInfo, Uptime: uptime,
			CPUUsage: cVal, MemUsedPercent: vm.UsedPercent, DiskUsedPercent: du.UsedPercent,
			NetInSpeed: spIn, NetOutSpeed: spOut, 
			NetTotalIn: curIn, NetTotalOut: curOut,
			PingResults: pingResults,
		}
		
		d, _ := json.Marshal(s)
		resp, err := client.Post(url, "application/json", bytes.NewBuffer(d))
		
		if err == nil { 
			body, _ := ioutil.ReadAll(resp.Body)
			resp.Body.Close()
			var serverResp AgentResponse
			if json.Unmarshal(body, &serverResp) == nil {
				// 更新全局目标列表
				if len(serverResp.PingTargets) > 0 {
					newStr, _ := json.Marshal(serverResp.PingTargets)
					oldStr, _ := json.Marshal(currentTargets)
					if string(newStr) != string(oldStr) {
						fmt.Printf("Config Update: Targets -> %s\n", newStr)
						currentTargets = serverResp.PingTargets
					}
				}
			}
		}
		
		time.Sleep(2 * time.Second)
	}
}

// 执行系统 Ping 命令
func execPing(ip string) int64 {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("ping", "-n", "1", "-w", "1000", ip)
	} else {
		cmd = exec.Command("ping", "-c", "1", "-W", "1", ip)
	}
	
	out, err := cmd.CombinedOutput()
	if err != nil { return 0 }
	
	// 正则匹配 time=xxx ms
	// 兼容: time=12.3 ms, 时间=12ms
	re := regexp.MustCompile(`(?i)(?:time|时间)[=<]([\d\.]+)`)
	matches := re.FindStringSubmatch(string(out))
	if len(matches) > 1 {
		val, _ := strconv.ParseFloat(matches[1], 64)
		return int64(val)
	}
	return 0
}
