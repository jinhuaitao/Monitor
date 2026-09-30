package main

// ================= 来源地址解析 =================
//
// 面板里有两处强依赖「真实的客户端地址」：
//   - 登录限流按来源计数
//   - 审计日志记录操作来源
//
// 这两种场景对「能不能采信转发头」的要求完全相反，所以必须显式区分：
//
//	直连时 —— RemoteAddr 就是真实对端，而 X-Forwarded-For 是请求方随手
//	          就能编的请求头。此时【必须】忽略转发头，否则按 IP 限流
//	          形同虚设（换个头就换了个身份）。
//	反代后 —— RemoteAddr 是代理自己的地址（同机部署时恒为 127.0.0.1）。
//	          此时若还只看 RemoteAddr，全部访客会共用一个限流桶：
//	          任何人在登录页失败 5 次，就能把【所有管理员】锁在门外
//	          10 分钟；而且审计日志里每一行来源都是 127.0.0.1，
//	          出事时完全无法回溯。
//
// 判据因此是「对端是否在可信代理列表里」，而不是「有没有转发头」。
// 只有对端本身可信时才去解析转发头，且必须从右往左取第一个非可信地址 ——
// Nginx 的 $proxy_add_x_forwarded_for 是【追加】语义，客户端自带的伪造值
// 落在最左边，真实地址在右边。取最左值是这类实现最常见的错误，等于把
// 伪造权又还给了攻击者。

import (
	"fmt"
	"log"
	"net"
	"os"
	"strings"

	"github.com/gin-gonic/gin"
)

// trustedProxies 可信代理网段。启动时解析一次，运行期只读。
var trustedProxies = loadTrustedProxies()

func loadTrustedProxies() []*net.IPNet {
	spec := strings.TrimSpace(os.Getenv("TRUSTED_PROXIES"))
	if spec == "" {
		// 默认只信任回环。这覆盖了「Nginx 与面板同机」这一最常见部署，
		// 同时对「面板直接暴露在公网」没有任何影响 —— 那种情况下对端
		// 不可能是回环地址，转发头会被忽略。
		//
		// 刻意不提供 0.0.0.0/0 这种「全信任」写法：那等于把伪造权交出去。
		spec = "127.0.0.0/8,::1/128"
	}
	nets, err := parseCIDRList(spec)
	if err != nil {
		// 解析失败必须让启动失败。静默忽略会让管理员以为转发头已经生效，
		// 而日志里其实全是代理的地址 —— 排查时毫无线索。
		log.Fatalf("TRUSTED_PROXIES 配置无效: %v\n"+
			"格式示例: TRUSTED_PROXIES=127.0.0.0/8,10.0.0.0/8,192.168.0.0/16", err)
	}
	return nets
}

func parseCIDRList(spec string) ([]*net.IPNet, error) {
	var out []*net.IPNet
	for _, item := range strings.Split(spec, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		// 允许直接写单个 IP，自动补全掩码长度
		if !strings.Contains(item, "/") {
			ip := net.ParseIP(item)
			if ip == nil {
				return nil, fmt.Errorf("%q 不是合法的 IP 或 CIDR", item)
			}
			if ip.To4() != nil {
				item += "/32"
			} else {
				item += "/128"
			}
		}
		_, n, err := net.ParseCIDR(item)
		if err != nil {
			return nil, fmt.Errorf("%q 不是合法的 CIDR: %w", item, err)
		}
		out = append(out, n)
	}
	return out, nil
}

func ipInList(ip net.IP, list []*net.IPNet) bool {
	for _, n := range list {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// clientIP 返回真实客户端地址，供限流与审计使用。
//
// 注意与 gin 自带的 c.ClientIP() 的区别：这里在「对端可信」时会从右往左
// 跳过全部可信代理，取第一个非可信地址；而取最左值（把客户端自带的
// X-Forwarded-For 当成真实来源）是错的。
func clientIP(c *gin.Context) string {
	remote := remoteIPOf(c)
	if remote == nil {
		return cleanField(c.Request.RemoteAddr, 64)
	}

	// 对端不可信 → 完全忽略转发头
	if !ipInList(remote, trustedProxies) {
		return remote.String()
	}

	// 对端可信 → 从右往左找第一个非可信地址
	if xff := c.GetHeader("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		for i := len(parts) - 1; i >= 0; i-- {
			ip := net.ParseIP(strings.TrimSpace(parts[i]))
			if ip == nil {
				continue
			}
			if !ipInList(ip, trustedProxies) {
				return ip.String()
			}
		}
	}
	if xri := net.ParseIP(strings.TrimSpace(c.GetHeader("X-Real-IP"))); xri != nil {
		return xri.String()
	}
	return remote.String()
}

func remoteIPOf(c *gin.Context) net.IP {
	addr := c.Request.RemoteAddr
	if host, _, err := net.SplitHostPort(addr); err == nil {
		addr = host
	}
	return net.ParseIP(strings.TrimSpace(addr))
}

// trustedProxiesSpec 供管理界面展示当前生效的信任列表。
func trustedProxiesSpec() string {
	if len(trustedProxies) == 0 {
		return "（无，转发头一律忽略）"
	}
	parts := make([]string, 0, len(trustedProxies))
	for _, n := range trustedProxies {
		parts = append(parts, n.String())
	}
	return strings.Join(parts, ", ")
}
