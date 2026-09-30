package main

// ================= 更新链路单元测试 =================
//
// 自更新这条路径有个特点：它在开发机上几乎永远不会被执行到，一旦写错，
// 后果是「面板把自己弄没了、且没有面板可用来自救」。所以脚本的生成结果
// 必须用测试固定住 —— 尤其是「替换二进制」那几步的原子性。

import (
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// ================= 替换脚本 =================

func withArgs(t *testing.T, args []string, fn func()) {
	t.Helper()
	saved := os.Args
	os.Args = args
	defer func() { os.Args = saved }()
	fn()
}

// TestBuildUpdateScriptIsValidShell 用 sh -n 做语法校验。
// 脚本是拼字符串拼出来的，拼错一个引号就会在真正更新时把服务搞坏，
// 而这条路径平时跑不到。
func TestBuildUpdateScriptIsValidShell(t *testing.T) {
	cases := []struct {
		name     string
		newPath  string
		target   string
		service  string
		args     []string
		wantPort bool
	}{
		{
			name:    "面板自更新（带端口，走健康校验）",
			newPath: "/opt/monitor/monitor.update", target: "/opt/monitor/monitor",
			service: "monitor_server", args: []string{"monitor", "-mode", "server", "-port", "8080"},
			wantPort: true,
		},
		{
			name:    "端口用等号写法",
			newPath: "/opt/monitor/monitor.update", target: "/opt/monitor/monitor",
			service: "monitor_server", args: []string{"monitor", "-port=9090"},
			wantPort: true,
		},
		{
			name:    "Agent 自更新（无端口，不校验）",
			newPath: "/usr/local/bin/monitor.update", target: "/usr/local/bin/monitor",
			service: "monitor", args: []string{"monitor", "-mode", "agent"},
			wantPort: false,
		},
		{
			name:    "路径含空格与单引号",
			newPath: "/opt/my monitor/monitor.update", target: "/opt/my monitor/it's monitor",
			service: "monitor_server", args: []string{"monitor", "-port", "8080"},
			wantPort: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var script string
			withArgs(t, tc.args, func() {
				script = buildUpdateScript(tc.newPath, tc.target, tc.service)
			})

			path := filepath.Join(t.TempDir(), "upd.sh")
			if err := os.WriteFile(path, []byte(script), 0755); err != nil {
				t.Fatalf("写入脚本失败: %v", err)
			}
			// -n 只做语法解析，不执行
			out, err := exec.Command("/bin/sh", "-n", path).CombinedOutput()
			if err != nil {
				t.Fatalf("生成的脚本语法有误: %v\n%s\n--- 脚本 ---\n%s", err, out, script)
			}

			if hasHealthCheck(script) != tc.wantPort {
				t.Errorf("健康校验分支不符合预期（wantPort=%v）:\n%s", tc.wantPort, script)
			}
		})
	}
}

func hasHealthCheck(script string) bool {
	return strings.Contains(script, "/healthz")
}

// TestUpdateScriptIsAtomic 锁死「替换二进制必须用一次 mv，不能先 rm」。
//
// rename(2) 是原子的：新二进制直接覆盖目标目录项，运行中的旧进程继续持有
// 旧 inode 直到退出。而「先 rm 再 mv」会留下一个真实存在的窗口 ——
// 这期间目标文件不存在，一旦 mv 失败（磁盘满、权限被改、跨文件系统），
// 面板就永久失去了可执行文件，且没有任何自动恢复路径。
func TestUpdateScriptIsAtomic(t *testing.T) {
	var script string
	withArgs(t, []string{"monitor", "-port", "8080"}, func() {
		script = buildUpdateScript("/opt/monitor/monitor.update", "/opt/monitor/monitor", "monitor_server")
	})

	// 不允许出现「删除目标二进制」的动作
	for _, bad := range []string{
		"rm -f '/opt/monitor/monitor'\n",
		"rm '/opt/monitor/monitor'\n",
	} {
		if strings.Contains(script, bad) {
			t.Fatalf("替换前删除了目标二进制，存在「文件不存在」窗口:\n%s", script)
		}
	}

	// 必须有一条直接把新文件覆盖到目标的 mv
	if !strings.Contains(script, "mv -f '/opt/monitor/monitor.update' '/opt/monitor/monitor'") {
		t.Fatalf("缺少原子替换步骤:\n%s", script)
	}

	// 必须先留一份备份，健康校验失败时才有东西可回滚
	if !strings.Contains(script, "cp -f '/opt/monitor/monitor' '/opt/monitor/monitor.old'") {
		t.Fatalf("缺少更新前备份，失败后无法回滚:\n%s", script)
	}

	// 回滚分支必须存在
	if !strings.Contains(script, "mv -f '/opt/monitor/monitor.old' '/opt/monitor/monitor'") {
		t.Fatalf("缺少回滚步骤:\n%s", script)
	}
}

// TestUpdateScriptSkipsHealthCheckWithoutCurl 锁死「没有 curl 时不得做健康校验」。
//
// 缺 curl 的环境如果照样跑校验，会因为「永远探不通」而把一次成功的更新
// 回滚掉 —— 那比不校验更糟。
func TestUpdateScriptSkipsHealthCheckWithoutCurl(t *testing.T) {
	var script string
	withArgs(t, []string{"monitor", "-port", "8080"}, func() {
		script = buildUpdateScript("/a/b.update", "/a/b", "monitor_server")
	})
	if !strings.Contains(script, "command -v curl >/dev/null 2>&1") {
		t.Fatalf("健康校验前必须先确认 curl 存在:\n%s", script)
	}
}

// ================= 二进制体检 =================

func writeFakeELF(t *testing.T, path string, machine uint16, size int) {
	t.Helper()
	buf := make([]byte, size)
	if size >= 20 {
		copy(buf, []byte{0x7f, 'E', 'L', 'F'})
		buf[4] = 2 // EI_CLASS = 64 位
		binary.LittleEndian.PutUint16(buf[18:20], machine)
	}
	if err := os.WriteFile(path, buf, 0755); err != nil {
		t.Fatalf("写入测试文件失败: %v", err)
	}
}

// TestVerifyBinary 锁死「下发给节点的二进制必须先体检」。
//
// 面板会把这份文件推给所有节点，节点收到后直接替换自身并重启。
// 放进来一个错误页或架构不对的二进制，后果不是「某个节点更新失败」，
// 而是【所有】节点一起起不来。sha256 挡不住这类问题：坏源配坏哈希，
// 永远对得上。
func TestVerifyBinary(t *testing.T) {
	dir := t.TempDir()
	const big = 2 << 20 // 2MB，越过「至少 1MB」的门槛

	amd64 := filepath.Join(dir, "amd64")
	writeFakeELF(t, amd64, emX86_64, big)

	arm64 := filepath.Join(dir, "arm64")
	writeFakeELF(t, arm64, emAARCH64, big)

	tiny := filepath.Join(dir, "tiny")
	writeFakeELF(t, tiny, emX86_64, 4096)

	notELF := filepath.Join(dir, "notelf")
	if err := os.WriteFile(notELF, []byte(strings.Repeat("<!DOCTYPE html>", 200000)), 0644); err != nil {
		t.Fatal(err)
	}

	if err := verifyBinary(amd64, "amd64"); err != nil {
		t.Errorf("合法 amd64 二进制被拒: %v", err)
	}
	if err := verifyBinary(arm64, "arm64"); err != nil {
		t.Errorf("合法 arm64 二进制被拒: %v", err)
	}
	// 架构错配必须被发现
	if err := verifyBinary(amd64, "arm64"); err == nil {
		t.Error("amd64 二进制被当成 arm64 放行")
	}
	if err := verifyBinary(arm64, "amd64"); err == nil {
		t.Error("arm64 二进制被当成 amd64 放行")
	}
	// 截断 / 错误页必须被发现
	if err := verifyBinary(tiny, "amd64"); err == nil {
		t.Error("过小的文件被放行")
	}
	if err := verifyBinary(notELF, "amd64"); err == nil {
		t.Error("非 ELF 文件被放行")
	}
	if err := verifyBinary(filepath.Join(dir, "missing"), "amd64"); err == nil {
		t.Error("不存在的文件被放行")
	}
}

// ================= shell 转义 =================

func TestShellQuote(t *testing.T) {
	cases := map[string]string{
		"plain":     "'plain'",
		"a b":       "'a b'",
		"it's":      `'it'\''s'`,
		"":          "''",
		"$(id)":     "'$(id)'",
		"a`id`b":    "'a`id`b'",
		"line\nbrk": "'line\nbrk'",
	}
	for in, want := range cases {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

// ================= 服务名探测 =================

func TestDetectServiceNameFallsBack(t *testing.T) {
	// 目标路径不存在于任何 init 脚本中时，必须退回到调用方给的兜底名，
	// 否则重启命令会拼出一个不存在的服务名（表现为更新后服务没起来）。
	got := detectServiceName("/nonexistent/path/to/monitor", "monitor_server")
	if got != "monitor_server" {
		t.Fatalf("应退回兜底服务名，实际 %q", got)
	}
}
