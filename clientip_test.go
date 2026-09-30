package main

import (
	"net"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// ================= 可信代理列表解析 =================

func TestParseCIDRList(t *testing.T) {
	cases := []struct {
		name    string
		spec    string
		want    []string
		wantErr bool
	}{
		{name: "空串", spec: "", want: nil},
		{name: "仅分隔符", spec: " , , ", want: nil},
		{name: "裸 IPv4 自动补 /32", spec: "10.0.0.1", want: []string{"10.0.0.1/32"}},
		{name: "裸 IPv6 自动补 /128", spec: "::1", want: []string{"::1/128"}},
		{name: "CIDR", spec: "10.0.0.0/8", want: []string{"10.0.0.0/8"}},
		{name: "多个含空白", spec: " 127.0.0.0/8 , 10.0.0.0/8 ", want: []string{"127.0.0.0/8", "10.0.0.0/8"}},
		{name: "非法 IP", spec: "not-an-ip", wantErr: true},
		{name: "非法掩码", spec: "10.0.0.0/99", wantErr: true},
		{name: "混入非法项", spec: "10.0.0.0/8,bogus", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseCIDRList(tc.spec)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseCIDRList(%q) 期望报错，实际通过", tc.spec)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseCIDRList(%q) 意外报错: %v", tc.spec, err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("parseCIDRList(%q) 长度 = %d, 期望 %d", tc.spec, len(got), len(tc.want))
			}
			for i, w := range tc.want {
				if got[i].String() != w {
					t.Errorf("第 %d 项 = %q, 期望 %q", i, got[i].String(), w)
				}
			}
		})
	}
}

func TestIPInList(t *testing.T) {
	list, err := parseCIDRList("127.0.0.0/8,10.0.0.0/8,::1/128")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		ip   string
		want bool
	}{
		{"127.0.0.1", true},
		{"127.255.255.254", true},
		{"10.1.2.3", true},
		{"::1", true},
		{"8.8.8.8", false},
		{"192.168.1.1", false},
		{"11.0.0.1", false}, // 与 10.0.0.0/8 仅前缀相似
	}
	for _, tc := range cases {
		if got := ipInList(net.ParseIP(tc.ip), list); got != tc.want {
			t.Errorf("ipInList(%s) = %v, 期望 %v", tc.ip, got, tc.want)
		}
	}
}

// ================= clientIP 裁决逻辑 =================

// withTrustedProxies 临时替换全局可信代理列表，测试结束自动还原。
func withTrustedProxies(t *testing.T, spec string) {
	t.Helper()
	list, err := parseCIDRList(spec)
	if err != nil {
		t.Fatalf("测试用代理列表 %q 非法: %v", spec, err)
	}
	old := trustedProxies
	trustedProxies = list
	t.Cleanup(func() { trustedProxies = old })
}

func ctxWithHeaders(remoteAddr string, headers map[string]string) *gin.Context {
	c := newTestCtx(remoteAddr)
	for k, v := range headers {
		c.Request.Header.Set(k, v)
	}
	return c
}

// 核心安全断言：对端不可信时，转发头必须被完全忽略。
// 否则任何人在登录页改一个请求头就换了个限流身份，防护形同虚设。
func TestClientIPIgnoresForwardedHeadersFromUntrustedPeer(t *testing.T) {
	withTrustedProxies(t, "127.0.0.0/8,::1/128")

	c := ctxWithHeaders("203.0.113.9:54321", map[string]string{
		"X-Forwarded-For": "1.2.3.4, 5.6.7.8",
		"X-Real-IP":       "9.9.9.9",
	})
	if got := clientIP(c); got != "203.0.113.9" {
		t.Fatalf("对端不可信时 clientIP = %q, 期望真实对端 203.0.113.9", got)
	}
}

// 对端可信时，从右往左取第一个非可信地址。
// 取最左值是这类实现最常见的错误 —— 客户端自带的伪造值就落在最左边。
func TestClientIPTakesRightmostUntrustedHop(t *testing.T) {
	withTrustedProxies(t, "127.0.0.0/8,10.0.0.0/8")

	cases := []struct {
		name string
		xff  string
		want string
	}{
		{
			name: "单跳代理",
			xff:  "198.51.100.7",
			want: "198.51.100.7",
		},
		{
			name: "客户端伪造值在最左，必须被跳过",
			xff:  "6.6.6.6, 198.51.100.7",
			want: "198.51.100.7",
		},
		{
			name: "多级可信代理，取最右侧非可信",
			xff:  "6.6.6.6, 198.51.100.7, 10.0.0.5",
			want: "198.51.100.7",
		},
		{
			name: "全部可信时回落到对端",
			xff:  "10.0.0.5, 10.0.0.6",
			want: "127.0.0.1",
		},
		{
			name: "夹带非法项时跳过而非采信",
			xff:  "garbage, 198.51.100.7",
			want: "198.51.100.7",
		},
		{
			name: "带空格的追加格式",
			xff:  "  6.6.6.6 ,  198.51.100.7  ",
			want: "198.51.100.7",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := ctxWithHeaders("127.0.0.1:40000", map[string]string{"X-Forwarded-For": tc.xff})
			if got := clientIP(c); got != tc.want {
				t.Fatalf("clientIP = %q, 期望 %q", got, tc.want)
			}
		})
	}
}

func TestClientIPFallsBackToXRealIP(t *testing.T) {
	withTrustedProxies(t, "127.0.0.0/8,10.0.0.0/8")

	// XFF 缺失 → 用 X-Real-IP
	c := ctxWithHeaders("127.0.0.1:40000", map[string]string{"X-Real-IP": "198.51.100.7"})
	if got := clientIP(c); got != "198.51.100.7" {
		t.Fatalf("clientIP = %q, 期望 198.51.100.7", got)
	}

	// XFF 全在可信列表内（说明每一跳都是我们自己的代理）→ 回落到 X-Real-IP。
	// 注意这里必须把 10.0.0.0/8 也列入信任，否则 10.0.0.5 会被正当地
	// 当成真实客户端 —— 那正是另一条测试要覆盖的行为。
	c = ctxWithHeaders("127.0.0.1:40000", map[string]string{
		"X-Forwarded-For": "10.0.0.5",
		"X-Real-IP":       "198.51.100.7",
	})
	if got := clientIP(c); got != "198.51.100.7" {
		t.Fatalf("XFF 全可信时 clientIP = %q, 期望回落到 X-Real-IP 198.51.100.7", got)
	}
}

// 只有「在可信列表里」的跳才被跳过。默认配置（仅回环）下，XFF 里的
// 私网地址同样是不可信来源 —— 必须原样采信，不能因为「看起来像内网」就放过。
func TestClientIPOnlySkipsListedProxies(t *testing.T) {
	withTrustedProxies(t, "127.0.0.0/8")

	c := ctxWithHeaders("127.0.0.1:40000", map[string]string{"X-Forwarded-For": "10.0.0.5"})
	if got := clientIP(c); got != "10.0.0.5" {
		t.Fatalf("clientIP = %q, 期望 10.0.0.5（未列入信任的地址就是真实来源）", got)
	}
}

func TestClientIPNoHeaders(t *testing.T) {
	withTrustedProxies(t, "127.0.0.0/8,::1/128")

	if got := clientIP(newTestCtx("198.51.100.7:1234")); got != "198.51.100.7" {
		t.Errorf("无转发头时 = %q, 期望 198.51.100.7", got)
	}
	if got := clientIP(newTestCtx("[2001:db8::1]:1234")); got != "2001:db8::1" {
		t.Errorf("IPv6 对端 = %q, 期望 2001:db8::1", got)
	}
	// RemoteAddr 无端口（非标准但真实存在，例如 unix socket 或测试桩）
	if got := clientIP(newTestCtx("198.51.100.7")); got != "198.51.100.7" {
		t.Errorf("无端口 RemoteAddr = %q, 期望 198.51.100.7", got)
	}
}

// 全信任列表是必须避免的配置：它让任何对端都能伪造来源。
// 这里固化「默认配置不含通配」这一事实。
func TestDefaultTrustedProxiesIsLoopbackOnly(t *testing.T) {
	old := trustedProxies
	t.Cleanup(func() { trustedProxies = old })

	t.Setenv("TRUSTED_PROXIES", "")
	list := loadTrustedProxies()
	if len(list) != 2 {
		t.Fatalf("默认信任列表长度 = %d, 期望 2（127.0.0.0/8 与 ::1/128）", len(list))
	}
	for _, n := range list {
		ones, bits := n.Mask.Size()
		if bits > 0 && ones == 0 {
			t.Fatalf("默认信任列表含全通配网段 %s，等于放弃来源校验", n)
		}
		if !n.Contains(net.ParseIP("127.0.0.1")) && !n.Contains(net.ParseIP("::1")) {
			t.Errorf("默认信任列表含非回环网段 %s", n)
		}
	}
}

func TestTrustedProxiesSpecRendersForUI(t *testing.T) {
	withTrustedProxies(t, "127.0.0.0/8,10.0.0.0/8")
	spec := trustedProxiesSpec()
	if !strings.Contains(spec, "127.0.0.0/8") || !strings.Contains(spec, "10.0.0.0/8") {
		t.Fatalf("trustedProxiesSpec = %q, 期望同时含两个网段", spec)
	}

	withTrustedProxies(t, "")
	if spec := trustedProxiesSpec(); !strings.Contains(spec, "无") {
		t.Fatalf("空列表时 trustedProxiesSpec = %q, 期望提示无信任代理", spec)
	}
}

// 限流 key 与审计来源都必须走 clientIP：这里直接验证 loginKey 的接线。
func TestLoginKeyUsesClientIP(t *testing.T) {
	withTrustedProxies(t, "127.0.0.0/8")
	resetLoginGuard()

	a := ctxWithHeaders("127.0.0.1:40000", map[string]string{"X-Forwarded-For": "198.51.100.7"})
	b := ctxWithHeaders("127.0.0.1:40001", map[string]string{"X-Forwarded-For": "198.51.100.7"})
	if ka, kb := loginKey(a), loginKey(b); ka != kb || ka != "198.51.100.7" {
		t.Fatalf("同一真实来源应共用限流桶: ka=%q kb=%q", ka, kb)
	}

	// 反代下若退化成按 RemoteAddr 计数，两个不同来源会共用一个桶 ——
	// 这正是「一个人失败 5 次锁死所有管理员」的成因。
	d := ctxWithHeaders("127.0.0.1:40002", map[string]string{"X-Forwarded-For": "203.0.113.9"})
	if loginKey(d) == loginKey(a) {
		t.Fatal("不同真实来源被并入了同一个限流桶，反代部署下会互相锁死")
	}
}
