package main

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// 回归测试：锁住「节点地址取值」与「定位触发条件」这两处行为。
//
// 背景：早期实现用 c.ClientIP() 作为定位输入，而引擎又关掉了代理信任，
// 于是面板前面只要有反代，所有节点都会被定位成代理所在国（或拿到私网地址
// 直接定位失败），卡片上显示错误的国家旗帜。这里把修复后的判定规则钉住：
//   - 只有对端落在可信代理白名单里，才采信 X-Forwarded-For；
//   - 取从右往左第一个非可信地址，而不是最左边那个可伪造的值；
//   - 空地址 / 私网地址绝不发请求（空地址会让接口返回「面板自己」的位置）。
//
// 定位端点通过 geoEndpoint 注入本地服务，因此不依赖外网。

func mkCtx(remoteAddr string, headers map[string]string) *gin.Context {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	req := httptest.NewRequest("POST", "/api/report", nil)
	req.RemoteAddr = remoteAddr
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	c.Request = req
	return c
}

func TestRealClientIP(t *testing.T) {
	cases := []struct {
		name       string
		proxies    string
		remoteAddr string
		xff        string
		realIP     string
		want       string
	}{
		{name: "直连：忽略伪造的 XFF", proxies: "", remoteAddr: "203.0.113.9:5000", xff: "1.2.3.4", want: "203.0.113.9"},
		{name: "白名单内：采信 XFF", proxies: "172.17.0.1", remoteAddr: "172.17.0.1:5000", xff: "203.0.113.9", want: "203.0.113.9"},
		{name: "多跳：右侧是可信内部跳，继续往左", proxies: "172.17.0.1", remoteAddr: "172.17.0.1:5000", xff: "203.0.113.9, 172.17.0.1", want: "203.0.113.9"},
		{name: "白名单含整段内网：内网客户端会被当跳跳过（固有代价）", proxies: "172.17.0.1,10.0.0.0/8", remoteAddr: "172.17.0.1:5000", xff: "1.2.3.4, 10.1.2.3", want: "1.2.3.4"},
		{name: "白名单内但客户端伪造了左值", proxies: "172.17.0.1", remoteAddr: "172.17.0.1:5000", xff: "9.9.9.9, 203.0.113.9", want: "203.0.113.9"},
		{name: "对端不在白名单：不采信", proxies: "10.0.0.0/8", remoteAddr: "172.17.0.1:5000", xff: "203.0.113.9", want: "172.17.0.1"},
		{name: "XFF 全为可信：回落 X-Real-IP", proxies: "10.0.0.0/8,172.17.0.1", remoteAddr: "172.17.0.1:5000", xff: "10.1.2.3", realIP: "203.0.113.7", want: "203.0.113.7"},
		{name: "IPv4-mapped 归一化", proxies: "", remoteAddr: "[::ffff:203.0.113.9]:5000", want: "203.0.113.9"},
	}
	for _, tc := range cases {
		setTrustedProxies(tc.proxies)
		h := map[string]string{}
		if tc.xff != "" {
			h["X-Forwarded-For"] = tc.xff
		}
		if tc.realIP != "" {
			h["X-Real-IP"] = tc.realIP
		}
		if got := realClientIP(mkCtx(tc.remoteAddr, h)); got != tc.want {
			t.Errorf("%s: realClientIP() = %q, want %q", tc.name, got, tc.want)
		}
	}
	setTrustedProxies("")
}

func TestIsPublicIP(t *testing.T) {
	bad := []string{"", "127.0.0.1", "10.1.2.3", "172.17.0.1", "192.168.1.1",
		"169.254.1.1", "100.64.0.1", "::1", "fe80::1", "not-an-ip"}
	for _, ip := range bad {
		if isPublicIP(ip) {
			t.Errorf("isPublicIP(%q) = true, want false", ip)
		}
	}
	for _, ip := range []string{"8.8.8.8", "203.0.113.9", "2606:4700::1111"} {
		if !isPublicIP(ip) {
			t.Errorf("isPublicIP(%q) = false, want true", ip)
		}
	}
}

func TestNormalizeProxyList(t *testing.T) {
	if _, err := normalizeProxyList("0.0.0.0/0"); err == nil {
		t.Error("必须拒绝 0.0.0.0/0")
	}
	if _, err := normalizeProxyList("::/0"); err == nil {
		t.Error("必须拒绝 ::/0")
	}
	if _, err := normalizeProxyList("hello"); err == nil {
		t.Error("必须拒绝无法识别的条目")
	}
	got, err := normalizeProxyList(" 172.17.0.1 , 10.0.0.0/8; 172.17.0.1 ")
	if err != nil {
		t.Fatal(err)
	}
	if got != "172.17.0.1,10.0.0.0/8" {
		t.Errorf("normalizeProxyList() = %q", got)
	}
	if s, err := normalizeProxyList(""); err != nil || s != "" {
		t.Errorf("空值应被接受并归一化为空串，得到 %q / %v", s, err)
	}
}

func TestNeedGeoLookup(t *testing.T) {
	now := time.Now()
	if !needGeoLookup(Node{}, "203.0.113.9") {
		t.Error("首次上报应触发解析")
	}
	done := Node{CountryCode: "US", LastGeoIP: "203.0.113.9", GeoAt: now}
	if needGeoLookup(done, "203.0.113.9") {
		t.Error("刚解析成功且地址未变，不应重复解析")
	}
	if !needGeoLookup(done, "198.51.100.1") {
		t.Error("地址变化必须重新解析")
	}
	if needGeoLookup(Node{LastGeoIP: "203.0.113.9", GeoAt: now}, "203.0.113.9") {
		t.Error("刚判定为私网/失败，未到重试窗口，不应重复解析")
	}
	failed := Node{LastGeoIP: "10.0.0.1", GeoAt: now.Add(-2 * time.Hour)}
	if needGeoLookup(failed, "10.0.0.1") {
		t.Error("失败后 2 小时，未到 6 小时重试窗口，不应重复解析")
	}
	failed.GeoAt = now.Add(-7 * time.Hour)
	if !needGeoLookup(failed, "10.0.0.1") {
		t.Error("失败后超过 6 小时应重试")
	}
	stale := Node{CountryCode: "US", LastGeoIP: "203.0.113.9", GeoAt: now.Add(-8 * 24 * time.Hour)}
	if !needGeoLookup(stale, "203.0.113.9") {
		t.Error("超过 7 天应复查")
	}
}

// 用本地 HTTP 服务把「发起请求 -> 解析响应 -> 写库」这条链路整条走通，
// 顺带确认空地址与私网地址根本不会发出请求。
func TestGeoLookupWritesCountry(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		switch r.URL.Path {
		case "/json/203.0.113.9":
			w.Write([]byte(`{"status":"success","countryCode":"jp"}`))
		case "/json/198.51.100.7":
			w.Write([]byte(`{"status":"fail","message":"reserved range"}`))
		default:
			w.WriteHeader(500)
		}
	}))
	defer srv.Close()

	old := geoEndpoint
	geoEndpoint = srv.URL + "/json/"
	defer func() { geoEndpoint = old }()

	var err error
	db, err = gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&Node{}); err != nil {
		t.Fatal(err)
	}
	db.Create(&Node{AgentID: "n1"})
	db.Create(&Node{AgentID: "n2"})

	geoLookupAsync("n1", "203.0.113.9")  // 正常：应写入 JP
	geoLookupAsync("n2", "198.51.100.7") // status=fail：不应写入国家码
	geoLookupAsync("n3", "")             // 空地址：绝不能发请求（否则查到的是面板自己）
	geoLookupAsync("n4", "192.168.1.5")  // 私网：绝不能发请求

	deadline := time.Now().Add(3 * time.Second)
	var n1 Node
	for time.Now().Before(deadline) {
		db.First(&n1, "agent_id = ?", "n1")
		if n1.CountryCode != "" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	var n2 Node
	db.First(&n2, "agent_id = ?", "n2")

	if n1.CountryCode != "JP" {
		t.Errorf("小写国家码应归一化为大写，得到 %q", n1.CountryCode)
	}
	if n1.LastGeoIP != "203.0.113.9" {
		t.Errorf("应记录解析所用地址，得到 %q", n1.LastGeoIP)
	}
	if n2.CountryCode != "" {
		t.Errorf("status=fail 时不应写入国家码，得到 %q", n2.CountryCode)
	}
	if n2.LastGeoIP != "198.51.100.7" {
		t.Error("解析失败也应打点，否则每轮心跳都会重试")
	}
	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Errorf("只应对两个公网地址发起请求，实际发出 %d 次", got)
	}
	if len(statusCache) > 0 {
		t.Errorf("节点不在缓存时不应写入缓存，缓存大小 %d", len(statusCache))
	}
}
