package main

// ================= PWA 支持 =================
//
// 目标：让面板可以「添加到主屏幕 / 安装为应用」并以独立窗口运行，
// 同时提供一个离线兜底页。
//
// 三个设计取舍，都是为了不破坏现有的「单文件二进制」部署方式：
//
//  1. 图标不用任何二进制资源文件，而是在运行时用 image/png 现画
//     （渐变圆角底 + 白色闪电）。这样 Dockerfile / install.sh / 发布流程
//     全都不用改，也不存在「资源文件没打包进去」的坑。
//     代价是首次请求要算一次，所以注册路由时会后台预热。
//
//  2. 页面导航（HTML）一律不写入缓存。面板 HTML 里内联了 Token、
//     面板地址等管理员配置，落盘缓存既可能读到陈旧配置，也容易在
//     共用设备上泄露。离线时统一回退到预缓存的离线页。
//
//  3. /api/ 请求完全放行：不拦截、不缓存、不兜底。
//     监控数据的价值全在「实时」，给一份过期数据比给错误提示更糟。

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
)

// ================= Manifest =================

const pwaManifest = `{
  "id": "/",
  "name": "Hub Monitor 服务器监控",
  "short_name": "Hub Monitor",
  "description": "轻量级服务器监控面板：CPU / 内存 / 磁盘 / 流量 / 网络延迟实时看板",
  "lang": "zh-CN",
  "dir": "ltr",
  "start_url": "/",
  "scope": "/",
  "display": "standalone",
  "display_override": ["standalone", "minimal-ui"],
  "orientation": "any",
  "background_color": "#eaf0f8",
  "theme_color": "#4f46e5",
  "categories": ["utilities", "productivity"],
  "prefer_related_applications": false,
  "icons": [
    { "src": "/icons/icon-192.png", "sizes": "192x192", "type": "image/png", "purpose": "any" },
    { "src": "/icons/icon-512.png", "sizes": "512x512", "type": "image/png", "purpose": "any" },
    { "src": "/icons/icon-maskable-512.png", "sizes": "512x512", "type": "image/png", "purpose": "maskable" }
  ],
  "shortcuts": [
    { "name": "系统管理", "short_name": "管理", "url": "/?action=settings" }
  ]
}`

// ================= Service Worker =================
//
// 缓存策略分三类，见 fetch 处理器内注释。
// 注意：本字符串会被注入版本号，因此每次发布新版本，SW 文件内容都会变化，
// 浏览器据此触发更新，旧缓存随 activate 清理，不会出现「装了新版却还在跑旧资源」。

const serviceWorkerTmpl = `/* Hub Monitor Service Worker */
const VERSION = '__VERSION__';
const CACHE = 'hub-monitor-' + VERSION;
const OFFLINE_URL = '/offline.html';

// 预缓存：体积小、且离线时必须可用的资源
const PRECACHE = [
  OFFLINE_URL,
  '/manifest.webmanifest',
  '/icons/icon-192.png',
  '/icons/favicon.svg'
];

// 第三方 CDN（图表库 / 字体）：跨域响应为 opaque，只能整体存取
const CDN_HOSTS = ['cdn.jsdelivr.net', 'fonts.googleapis.com', 'fonts.gstatic.com'];

self.addEventListener('install', function (e) {
  e.waitUntil((async function () {
    const cache = await caches.open(CACHE);
    await Promise.all(PRECACHE.map(function (u) {
      return cache.add(new Request(u, { cache: 'reload' })).catch(function () {});
    }));
    await self.skipWaiting();
  })());
});

self.addEventListener('activate', function (e) {
  e.waitUntil((async function () {
    const keys = await caches.keys();
    await Promise.all(keys.map(function (k) {
      if (k !== CACHE && k.indexOf('hub-monitor-') === 0) return caches.delete(k);
      return Promise.resolve();
    }));
    await self.clients.claim();
  })());
});

self.addEventListener('fetch', function (event) {
  const req = event.request;
  if (req.method !== 'GET') return;

  let url;
  try { url = new URL(req.url); } catch (err) { return; }

  // ① 实时接口：直接放行，交给页面自身的轮询逻辑处理
  if (url.origin === self.location.origin && url.pathname.indexOf('/api/') === 0) return;

  // ①' 健康探针：离线页靠它判断面板是否恢复，必须打到真实服务端
  if (url.origin === self.location.origin && url.pathname === '/healthz') return;

  // ② 页面导航：网络优先；断网时回退离线页（不缓存 HTML 本身，见源码注释）
  if (req.mode === 'navigate') {
    event.respondWith((async function () {
      try {
        return await fetch(req);
      } catch (err) {
        const cache = await caches.open(CACHE);
        const off = await cache.match(OFFLINE_URL);
        if (off) return off;
        return new Response('离线', {
          status: 503,
          headers: { 'Content-Type': 'text/plain; charset=utf-8' }
        });
      }
    })());
    return;
  }

  // ③ 静态资源：缓存优先 + 后台静默更新（stale-while-revalidate）
  const sameOriginStatic = url.origin === self.location.origin &&
    /^\/(icons\/|manifest\.webmanifest|offline\.html|favicon\.ico)/.test(url.pathname);
  if (sameOriginStatic || CDN_HOSTS.indexOf(url.hostname) >= 0) {
    event.respondWith((async function () {
      const cache = await caches.open(CACHE);
      const hit = await cache.match(req);
      const net = fetch(req).then(function (res) {
        if (res && (res.ok || res.type === 'opaque')) cache.put(req, res.clone());
        return res;
      }).catch(function () { return null; });
      if (hit) return hit;
      const res = await net;
      if (res) return res;
      return new Response('', { status: 504 });
    })());
  }
});
`

func serviceWorkerJS() string {
	return strings.ReplaceAll(serviceWorkerTmpl, "__VERSION__", displayVersion())
}

// ================= 离线页 =================

const htmlOffline = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1, viewport-fit=cover">
<title>离线 · Hub Monitor</title>
<style>
:root{
  --bg:#eaf0f8; --fg:#0f172a; --sub:#5b6b85; --mute:#94a3b8;
  --card:rgba(255,255,255,.72); --border:rgba(255,255,255,.75);
  --soft:rgba(15,23,42,.05);
  --shadow:0 24px 60px -22px rgba(15,23,42,.4);
  --primary:#4f46e5; --primary-2:#8b5cf6;
}
@media (prefers-color-scheme:dark){
  :root{
    --bg:#070b16; --fg:#e9eefb; --sub:#9fb0cc; --mute:#6b7c99;
    --card:rgba(20,28,48,.72); --border:rgba(255,255,255,.09);
    --soft:rgba(255,255,255,.05);
    --shadow:0 26px 66px -24px rgba(0,0,0,.9);
  }
}
*{box-sizing:border-box}
body{
  margin:0;min-height:100vh;display:flex;align-items:center;justify-content:center;padding:24px;
  background:var(--bg);color:var(--fg);-webkit-font-smoothing:antialiased;
  font-family:-apple-system,BlinkMacSystemFont,'Segoe UI','PingFang SC','Microsoft YaHei',sans-serif;
}
.card{
  width:100%;max-width:392px;padding:40px 32px;text-align:center;border-radius:26px;
  background:var(--card);border:1px solid var(--border);box-shadow:var(--shadow);
  -webkit-backdrop-filter:blur(24px) saturate(160%);backdrop-filter:blur(24px) saturate(160%);
}
.badge{
  width:56px;height:56px;margin:0 auto 18px;border-radius:17px;display:flex;align-items:center;
  justify-content:center;font-size:27px;color:#fff;
  background:linear-gradient(135deg,var(--primary),var(--primary-2));
  box-shadow:0 12px 26px -8px rgba(79,70,229,.65);
}
h1{margin:0 0 10px;font-size:20px;font-weight:800;letter-spacing:-.4px}
p{margin:0;font-size:13.5px;line-height:1.7;color:var(--sub)}
.hint{margin-top:20px;padding:12px 14px;border-radius:14px;background:var(--soft);
  font-size:12.5px;line-height:1.7;color:var(--sub);text-align:left}
button{
  margin-top:22px;width:100%;padding:13px;border:none;border-radius:14px;cursor:pointer;
  font-size:15px;font-weight:700;color:#fff;font-family:inherit;letter-spacing:.3px;
  background:linear-gradient(135deg,var(--primary),var(--primary-2));
  box-shadow:0 8px 20px -8px rgba(79,70,229,.7);transition:transform .2s,filter .2s;
}
button:hover{transform:translateY(-2px);filter:brightness(1.06)}
button:active{transform:translateY(0)}
.dot{display:inline-block;width:7px;height:7px;border-radius:50%;background:var(--mute);margin-right:6px}
.dot.on{background:#10b981;box-shadow:0 0 8px #10b981}
</style>
</head>
<body>
<div class="card">
  <div class="badge">⚡</div>
  <h1>连接已断开</h1>
  <p>当前设备处于离线状态，无法读取面板的实时数据。</p>
  <div class="hint">
    <span class="dot" id="netDot"></span><span id="netText">正在检测…</span><br>
    Hub Monitor 展示的是服务器实时指标，必须联网才能获取 —— 因此这里不做数据缓存，避免让你看到过期的数字。
  </div>
  <button onclick="location.reload()">立即重新连接</button>
</div>
<script>
/* 这个页面最常见的触发场景其实是「面板正在更新重启」，
   所以除了让用户手动点，还做一次有限次数的自动重连，
   面板一恢复，独立窗口就自己回来了。 */
var tries = 0, MAX_TRIES = 36, probing = false;
var dot = document.getElementById('netDot'), txt = document.getElementById('netText');

function state(ok, text){
  dot.className = 'dot' + (ok ? ' on' : '');
  txt.textContent = text;
}

/* 探针刻意打 /healthz：它不在 Service Worker 的缓存白名单里，
   请求会真的落到服务端 —— 否则探针会被 SW 用缓存应答，永远"探测成功" */
function probe(){
  if (probing) return;
  probing = true;
  fetch('/healthz?t=' + Date.now(), { cache: 'no-store' })
    .then(function(r){
      if (!r.ok) throw new Error('bad status');
      state(true, '面板已恢复，正在重新连接…');
      location.reload();
    })
    .catch(function(){
      probing = false;
      tries++;
      if (!navigator.onLine){
        state(false, '设备未连接到网络，请检查 Wi-Fi 或移动数据');
      } else if (tries >= MAX_TRIES){
        state(false, '仍无法连接到面板服务，请确认服务端是否在运行');
        clearInterval(timer);
      } else {
        state(false, '无法连接到面板服务，正在自动重试（第 ' + tries + ' 次）…');
      }
    });
}

state(false, '正在检测…');
probe();
var timer = setInterval(probe, 5000);
window.addEventListener('online', function(){ tries = 0; probe(); });
window.addEventListener('offline', function(){
  state(false, '设备未连接到网络，请检查 Wi-Fi 或移动数据');
});
</script>
</body>
</html>
`

// ================= 图标（运行时绘制） =================

const faviconSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64" width="64" height="64">
  <defs>
    <linearGradient id="g" x1="0" y1="0" x2="1" y2="1">
      <stop offset="0" stop-color="#4f46e5"/>
      <stop offset="1" stop-color="#8b5cf6"/>
    </linearGradient>
  </defs>
  <rect x="0" y="0" width="64" height="64" rx="14" fill="url(#g)"/>
  <path d="M36 7 L15 35 L29 35 L25 57 L49 28 L34.5 28 L41 7 Z" fill="#fff"/>
</svg>`

// boltPolygon 闪电轮廓，坐标归一化到 0..1 的方形内（顶点顺序不可打乱）
func boltPolygon() [][2]float64 {
	return [][2]float64{
		{0.58, 0.00}, {0.16, 0.56}, {0.44, 0.56},
		{0.36, 1.00}, {0.84, 0.42}, {0.55, 0.42}, {0.68, 0.00},
	}
}

// pointInPoly 射线法判断点是否在多边形内
func pointInPoly(x, y float64, poly [][2]float64) bool {
	in := false
	for i, j := 0, len(poly)-1; i < len(poly); j, i = i, i+1 {
		xi, yi := poly[i][0], poly[i][1]
		xj, yj := poly[j][0], poly[j][1]
		if (yi > y) != (yj > y) && x < (xj-xi)*(y-yi)/(yj-yi)+xi {
			in = !in
		}
	}
	return in
}

// insideRoundedRect 判断点是否落在圆角正方形内（r=0 即直角）
func insideRoundedRect(x, y, size, r float64) bool {
	if x < 0 || y < 0 || x >= size || y >= size {
		return false
	}
	if r <= 0 {
		return true
	}
	var dx, dy float64
	if x < r {
		dx = r - x
	} else if x > size-r {
		dx = x - (size - r)
	} else {
		return true
	}
	if y < r {
		dy = r - y
	} else if y > size-r {
		dy = y - (size - r)
	} else {
		return true
	}
	return dx*dx+dy*dy <= r*r
}

// renderIconPNG 现画一枚图标：对角渐变圆角底 + 白色闪电。
//
// 做法是先在放大 ss 倍的画布上做「是否命中」判断，再盒式降采样求覆盖率，
// 边缘因此带抗锯齿 —— 纯逐像素二值填充会让圆角和闪电斜边出现明显锯齿。
// maskable 图标必须满幅铺底（由系统遮罩裁切），且图形要缩进安全区，
// 否则被裁成圆形时闪电的尖角会被切掉。
func renderIconPNG(size int, maskable bool) []byte {
	const ss = 3
	w := size * ss

	radius := float64(w) * 0.22
	inset := 0.11
	if maskable {
		radius = 0 // 满幅，交给系统遮罩
		inset = 0.21
	}
	span := 1 - 2*inset

	poly := boltPolygon()
	bgCov := make([]float64, size*size)
	fgCov := make([]float64, size*size)

	for y := 0; y < w; y++ {
		fy := float64(y) + 0.5
		ny := (fy/float64(w) - inset) / span
		rowBase := (y / ss) * size
		for x := 0; x < w; x++ {
			fx := float64(x) + 0.5
			i := rowBase + x/ss
			if insideRoundedRect(fx, fy, float64(w), radius) {
				bgCov[i]++
			}
			if ny >= 0 && ny <= 1 {
				nx := (fx/float64(w) - inset) / span
				if nx >= 0 && nx <= 1 && pointInPoly(nx, ny, poly) {
					fgCov[i]++
				}
			}
		}
	}

	const denom = float64(ss * ss)
	max := float64(size - 1)
	img := image.NewNRGBA(image.Rect(0, 0, size, size))

	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			i := y*size + x
			a := bgCov[i] / denom
			if a <= 0 {
				continue // 圆角外保持全透明
			}
			// 对角渐变 #4f46e5 → #8b5cf6
			t := (float64(x) + float64(y)) / (2 * max)
			r := 0x4f + (0x8b-0x4f)*t
			g := 0x46 + (0x5c-0x46)*t
			b := 0xe5 + (0xf6-0xe5)*t

			f := fgCov[i] / denom
			if f > a {
				f = a
			}
			if f > 0 {
				// 闪电为纯白：按其在像素内的占比与底色混合
				k := f / a
				r += (255 - r) * k
				g += (255 - g) * k
				b += (255 - b) * k
			}
			img.SetNRGBA(x, y, color.NRGBA{
				R: uint8(r + 0.5), G: uint8(g + 0.5), B: uint8(b + 0.5),
				A: uint8(a*255 + 0.5),
			})
		}
	}

	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	if err := enc.Encode(&buf, img); err != nil {
		return nil
	}
	return buf.Bytes()
}

var pwaIcons = struct {
	sync.Mutex
	m map[string][]byte
}{m: make(map[string][]byte)}

// iconBytes 带缓存地取图标；同一尺寸只绘制一次
func iconBytes(size int, maskable bool) []byte {
	key := fmt.Sprintf("%d/%v", size, maskable)
	pwaIcons.Lock()
	defer pwaIcons.Unlock()
	if b, ok := pwaIcons.m[key]; ok {
		return b
	}
	b := renderIconPNG(size, maskable)
	pwaIcons.m[key] = b
	return b
}

// ================= 路由注册 =================

func registerPWARoutes(r *gin.Engine) {
	r.GET("/manifest.webmanifest", func(c *gin.Context) {
		c.Header("Cache-Control", "no-cache")
		c.Data(http.StatusOK, "application/manifest+json; charset=utf-8", []byte(pwaManifest))
	})

	// 健康探针：只回一个 "ok"，供离线页判断面板是否已恢复。
	// 刻意不放进 Service Worker 的缓存白名单 —— 探针必须真的打到服务端，
	// 否则会被 SW 用缓存应答，永远"探测成功"。
	r.GET("/healthz", func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		c.String(http.StatusOK, "ok")
	})

	// Service Worker 必须挂在根路径，作用域才能覆盖整站；
	// 且不能长缓存，否则新版本发布后浏览器一直拿旧脚本。
	r.GET("/sw.js", func(c *gin.Context) {
		c.Header("Cache-Control", "no-cache, no-store, must-revalidate")
		c.Header("Service-Worker-Allowed", "/")
		c.Data(http.StatusOK, "application/javascript; charset=utf-8", []byte(serviceWorkerJS()))
	})

	r.GET("/offline.html", func(c *gin.Context) {
		c.Header("Cache-Control", "public, max-age=600")
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(htmlOffline))
	})

	icon := func(size int, maskable bool) gin.HandlerFunc {
		return func(c *gin.Context) {
			body := iconBytes(size, maskable)
			if len(body) == 0 {
				c.Status(http.StatusInternalServerError)
				return
			}
			c.Header("Cache-Control", "public, max-age=604800")
			c.Data(http.StatusOK, "image/png", body)
		}
	}
	r.GET("/icons/icon-192.png", icon(192, false))
	r.GET("/icons/icon-512.png", icon(512, false))
	r.GET("/icons/icon-maskable-512.png", icon(512, true))
	r.GET("/icons/apple-touch-icon.png", icon(180, false))
	r.GET("/favicon.ico", icon(64, false))

	r.GET("/icons/favicon.svg", func(c *gin.Context) {
		c.Header("Cache-Control", "public, max-age=604800")
		c.Data(http.StatusOK, "image/svg+xml; charset=utf-8", []byte(faviconSVG))
	})

	// 后台预热：512 那两张要算几百万次多边形判定，
	// 放到启动时算掉，避免第一个请求慢半拍
	go func() {
		for _, it := range []struct {
			size     int
			maskable bool
		}{{192, false}, {180, false}, {64, false}, {512, false}, {512, true}} {
			iconBytes(it.size, it.maskable)
		}
	}()
}
