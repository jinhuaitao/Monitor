package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// ================= 更新子系统 =================
//
// 1. 检查更新：拉取 GitHub Release（支持加速镜像），比对本地版本
// 2. 面板自更新：下载对应架构的二进制 -> 校验 -> 替换自身 -> 重启服务
// 3. 客户端更新：面板预下载各架构 Agent 二进制 -> 通过心跳下发更新指令 -> Agent 自更新

const (
	defaultRepo   = "jinhuaitao/Monitor"
	agentDirName  = "agents"
	binaryPrefix  = "monitor-linux-"
	supportedArch = "amd64,arm64"
)

// ReleaseAsset 发布资源
type ReleaseAsset struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	URL  string `json:"url"`
	SHA  string `json:"sha"`
}

// ReleaseInfo 远端版本信息
type ReleaseInfo struct {
	Version     string         `json:"version"`
	Tag         string         `json:"tag"`
	PublishedAt string         `json:"published_at"`
	Notes       string         `json:"notes"`
	URL         string         `json:"url"`
	Assets      []ReleaseAsset `json:"assets"`
}

// UpdateTaskState 更新任务状态（供前端轮询）
type UpdateTaskState struct {
	Busy    bool   `json:"busy"`
	Stage   string `json:"stage"`
	Percent int    `json:"percent"`
	Error   string `json:"error"`
	Done    bool   `json:"done"`
	Message string `json:"message"`
}

var updateState struct {
	sync.Mutex
	UpdateTaskState
}

func setUpdateState(stage string, percent int) {
	updateState.Lock()
	updateState.Busy = true
	updateState.Stage = stage
	updateState.Percent = percent
	updateState.Error = ""
	updateState.Done = false
	updateState.Unlock()
}

func failUpdateState(msg string) {
	updateState.Lock()
	updateState.Busy = false
	updateState.Error = msg
	updateState.Done = true
	updateState.Unlock()
}

func doneUpdateState(msg string) {
	updateState.Lock()
	updateState.Busy = false
	updateState.Percent = 100
	updateState.Stage = "完成"
	updateState.Message = msg
	updateState.Done = true
	updateState.Unlock()
}

func getUpdateState() UpdateTaskState {
	updateState.Lock()
	defer updateState.Unlock()
	return updateState.UpdateTaskState
}

// ================= 配置读取 =================

func getUpdateRepo() string {
	globalConfig.RLock()
	defer globalConfig.RUnlock()
	if globalConfig.UpdateRepo == "" {
		return defaultRepo
	}
	return globalConfig.UpdateRepo
}

func getUpdateProxy() string {
	globalConfig.RLock()
	defer globalConfig.RUnlock()
	return strings.TrimSpace(globalConfig.UpdateProxy)
}

func getRestartCmd() string {
	globalConfig.RLock()
	defer globalConfig.RUnlock()
	return strings.TrimSpace(globalConfig.RestartCmd)
}

func getAgentBundleVersion() string {
	globalConfig.RLock()
	defer globalConfig.RUnlock()
	return globalConfig.AgentBundleVersion
}

// ================= 网络工具 =================

// withProxy 为 GitHub 地址套上加速镜像前缀
func withProxy(rawURL string) string {
	p := getUpdateProxy()
	if p == "" {
		return rawURL
	}
	if !strings.HasSuffix(p, "/") {
		p += "/"
	}
	return p + rawURL
}

var ghClient = &http.Client{Timeout: 30 * time.Second}

func ghGet(rawURL string) (*http.Response, error) {
	req, err := http.NewRequest("GET", rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "HubMonitor-Updater")
	req.Header.Set("Accept", "*/*")
	return ghClient.Do(req)
}

func ghGetText(rawURL string) string {
	// 先走镜像，失败回退直连
	urls := []string{withProxy(rawURL)}
	if withProxy(rawURL) != rawURL {
		urls = append(urls, rawURL)
	}
	for _, u := range urls {
		resp, err := ghGet(u)
		if err != nil {
			continue
		}
		if resp.StatusCode != 200 {
			resp.Body.Close()
			continue
		}
		b, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			continue
		}
		return strings.TrimSpace(string(b))
	}
	return ""
}

// ================= 版本检查 =================

var releaseCache struct {
	sync.Mutex
	at   time.Time
	info *ReleaseInfo
	err  error
}

// cachedLatestRelease 带 5 分钟缓存，避免频繁请求 GitHub
func cachedLatestRelease(force bool) (*ReleaseInfo, error) {
	releaseCache.Lock()
	if !force && releaseCache.info != nil && time.Since(releaseCache.at) < 5*time.Minute {
		info, err := releaseCache.info, releaseCache.err
		releaseCache.Unlock()
		return info, err
	}
	releaseCache.Unlock()

	info, err := fetchLatestRelease()

	releaseCache.Lock()
	releaseCache.at = time.Now()
	releaseCache.info = info
	releaseCache.err = err
	releaseCache.Unlock()
	return info, err
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func fetchLatestRelease() (*ReleaseInfo, error) {
	repo := getUpdateRepo()
	apiURL := "https://api.github.com/repos/" + repo + "/releases/latest"

	body := ""
	lastErr := fmt.Errorf("未知错误")
	for _, u := range []string{withProxy(apiURL), apiURL} {
		resp, err := ghGet(u)
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode != 200 {
			resp.Body.Close()
			lastErr = fmt.Errorf("GitHub 返回状态码 %d", resp.StatusCode)
			continue
		}
		b, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}
		body = string(b)
		break
	}
	if body == "" {
		return nil, fmt.Errorf("无法获取版本信息：%v（若在国内网络可在下方切换加速镜像）", lastErr)
	}

	var rel struct {
		TagName     string `json:"tag_name"`
		HTMLURL     string `json:"html_url"`
		PublishedAt string `json:"published_at"`
		Body        string `json:"body"`
		Assets      []struct {
			Name               string `json:"name"`
			Size               int64  `json:"size"`
			BrowserDownloadURL string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.Unmarshal([]byte(body), &rel); err != nil {
		return nil, fmt.Errorf("解析版本信息失败：%v", err)
	}

	info := &ReleaseInfo{
		Tag:         rel.TagName,
		URL:         rel.HTMLURL,
		PublishedAt: rel.PublishedAt,
		Notes:       truncateText(rel.Body, 600),
		Version:     rel.TagName,
	}
	for _, a := range rel.Assets {
		info.Assets = append(info.Assets, ReleaseAsset{
			Name: a.Name,
			Size: a.Size,
			URL:  a.BrowserDownloadURL, // 存直链，下载时再套镜像
		})
	}

	// 优先使用发布包中的 VERSION 文件（比 latest 标签更精确）
	if v := ghGetText("https://github.com/" + repo + "/releases/latest/download/VERSION"); v != "" {
		info.Version = firstLine(v)
	}

	// 读取 sha256 校验文件
	for i := range info.Assets {
		a := info.Assets[i]
		if !strings.HasSuffix(a.Name, ".sha256") {
			continue
		}
		base := strings.TrimSuffix(a.Name, ".sha256")
		if txt := ghGetText(a.URL); txt != "" {
			for j := range info.Assets {
				if info.Assets[j].Name == base {
					info.Assets[j].SHA = strings.Fields(txt)[0]
				}
			}
		}
	}
	return info, nil
}

func firstLine(s string) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}

func truncateText(s string, n int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func findAsset(info *ReleaseInfo, name string) *ReleaseAsset {
	if info == nil {
		return nil
	}
	for i := range info.Assets {
		if info.Assets[i].Name == name {
			return &info.Assets[i]
		}
	}
	return nil
}

// hasUpdate 判断远端是否比本地新（本地为 dev 时始终提示可更新）
func hasUpdate(local, remote string) bool {
	if remote == "" {
		return false
	}
	if local == "" || local == "dev" || local == "unknown" {
		return true
	}
	return strings.TrimSpace(local) != strings.TrimSpace(remote)
}

// ================= 下载 =================

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func downloadAsset(a *ReleaseAsset, dest string) error {
	// 先走镜像，失败自动回退直连
	urls := []string{withProxy(a.URL)}
	if withProxy(a.URL) != a.URL {
		urls = append(urls, a.URL)
	}

	var lastErr error
	for _, u := range urls {
		if err := tryDownload(u, dest, a.SHA); err != nil {
			lastErr = err
			continue
		}
		return nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("下载失败")
	}
	return lastErr
}

func tryDownload(url, dest, wantSHA string) error {
	resp, err := ghGet(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	tmp := dest + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return err
	}
	h := sha256.New()
	written, err := io.Copy(io.MultiWriter(f, h), resp.Body)
	f.Close()
	if err != nil {
		os.Remove(tmp)
		return err
	}
	if written == 0 {
		os.Remove(tmp)
		return fmt.Errorf("下载内容为空")
	}
	if wantSHA != "" {
		got := hex.EncodeToString(h.Sum(nil))
		if !strings.EqualFold(got, wantSHA) {
			os.Remove(tmp)
			return fmt.Errorf("校验失败 (%s ≠ %s)", got[:12], wantSHA[:12])
		}
	}
	if err := os.Chmod(tmp, 0755); err != nil {
		return err
	}
	return os.Rename(tmp, dest)
}

// ================= 自身路径 / 重启 =================

func selfPath() string {
	p, err := os.Executable()
	if err != nil {
		p, _ = filepath.Abs(os.Args[0])
	}
	if real, err := filepath.EvalSymlinks(p); err == nil && real != "" {
		p = real
	}
	return p
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func isDocker() bool {
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	return false
}

// detectServiceName 通过 init 脚本中记录的可执行文件路径反查真实服务名。
// 例如 /etc/init.d/monitor_server 中 command="/opt/monitor/monitor"，
// 或 /etc/systemd/system/xxx.service 中 ExecStart=/opt/monitor/monitor ...
// 这样即使用户自定义了服务名，重启命令也不会猜错（猜错会导致 nohup 兜底再拉一个实例、抢占端口）。
func detectServiceName(target string, fallbacks ...string) string {
	scan := func(dir, suffix string) string {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return ""
		}
		for _, e := range entries {
			name := e.Name()
			if suffix != "" && !strings.HasSuffix(name, suffix) {
				continue
			}
			b, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				continue // 目录或不可读文件
			}
			if strings.Contains(string(b), target) {
				return strings.TrimSuffix(name, suffix)
			}
		}
		return ""
	}
	if svc := scan("/etc/systemd/system", ".service"); svc != "" {
		return svc
	}
	if svc := scan("/etc/init.d", ""); svc != "" {
		return svc
	}
	for _, f := range fallbacks {
		if f != "" {
			return f
		}
	}
	return ""
}

// buildRestartLine 生成更新后的重启命令
func buildRestartLine(target, fallbackService string) string {
	if custom := getRestartCmd(); custom != "" {
		return custom
	}
	args := make([]string, 0, len(os.Args))
	for _, a := range os.Args[1:] {
		args = append(args, shellQuote(a))
	}
	argLine := strings.Join(args, " ")

	parts := make([]string, 0, 3)
	if svc := detectServiceName(target, fallbackService); svc != "" {
		parts = append(parts,
			fmt.Sprintf("systemctl restart %s >/dev/null 2>&1", shellQuote(svc)),
			fmt.Sprintf("rc-service %s restart >/dev/null 2>&1", shellQuote(svc)))
	}
	// 最后兜底：直接以后台方式拉起（日志写入 /tmp/monitor.log 便于排查）
	parts = append(parts, fmt.Sprintf("(nohup %s %s >>/tmp/monitor.log 2>&1 &)", shellQuote(target), argLine))
	return strings.Join(parts, " || ")
}

// applyUpdateAndRestart 用新二进制替换自身并重启（脚本会脱离父进程执行）
//
// 时序说明：调用方随即退出进程，脚本等待 3 秒确保旧进程已释放端口，
// 再替换二进制并拉起服务，避免新旧实例抢占监听端口。
func applyUpdateAndRestart(newPath, target, serviceName string) error {
	wd, _ := os.Getwd()
	script := "#!/bin/sh\n" +
		"sleep 3\n" +
		"rm -f " + shellQuote(target) + "\n" +
		"mv " + shellQuote(newPath) + " " + shellQuote(target) + "\n" +
		"chmod 755 " + shellQuote(target) + "\n" +
		"cd " + shellQuote(wd) + "\n" +
		buildRestartLine(target, serviceName) + "\n" +
		"rm -f \"$0\"\n"

	sp := filepath.Join(os.TempDir(), fmt.Sprintf("monitor-upd-%d.sh", os.Getpid()))
	if err := os.WriteFile(sp, []byte(script), 0755); err != nil {
		return err
	}

	cmd := exec.Command("/bin/sh", sp)
	cmd.Dir = wd
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		// 某些环境没有 /bin/sh 权限时回退直接执行
		cmd = exec.Command("sh", sp)
		if err2 := cmd.Start(); err2 != nil {
			return err
		}
	}
	return nil
}

// ================= 面板自更新 =================

func archOfSelf() string {
	switch runtime.GOARCH {
	case "amd64", "386":
		return "amd64"
	case "arm64", "aarch64":
		return "arm64"
	}
	return runtime.GOARCH
}

// startServerUpdate 异步执行面板自更新
func startServerUpdate() {
	go func() {
		setUpdateState("获取版本信息", 5)
		rel, err := fetchLatestRelease()
		if err != nil {
			failUpdateState(err.Error())
			return
		}
		if !hasUpdate(BuildVersion, rel.Version) {
			doneUpdateState("当前已是最新版本 " + rel.Version)
			return
		}
		if runtime.GOOS != "linux" {
			failUpdateState("在线自更新仅支持 Linux 运行环境")
			return
		}
		if isDocker() {
			failUpdateState("检测到 Docker 运行环境，请通过拉取新镜像完成升级（docker compose pull && up -d）")
			return
		}

		asset := findAsset(rel, binaryPrefix+archOfSelf())
		if asset == nil {
			failUpdateState("未找到适用于 " + archOfSelf() + " 的发布资源")
			return
		}

		target := selfPath()
		// 提前校验目录可写，避免下载完成后才发现无法替换
		if f, err := os.CreateTemp(filepath.Dir(target), ".hm-wtest"); err != nil {
			failUpdateState("面板所在目录不可写，请使用 root 或调整文件权限后重试")
			return
		} else {
			f.Close()
			os.Remove(f.Name())
		}

		setUpdateState("下载新版本", 20)
		tmp := target + ".update"
		os.Remove(tmp)
		if err := downloadAsset(asset, tmp); err != nil {
			os.Remove(tmp)
			failUpdateState("下载失败：" + err.Error())
			return
		}

		setUpdateState("安装并重启", 85)
		if err := applyUpdateAndRestart(tmp, target, "monitor_server"); err != nil {
			os.Remove(tmp)
			failUpdateState("安装失败：" + err.Error())
			return
		}
		doneUpdateState("更新完成，面板正在重启…")
		// 留一点时间让 HTTP 响应返回，然后退出等待服务管理器拉起
		time.Sleep(2 * time.Second)
		os.Exit(0)
	}()
}

// ================= 客户端 (Agent) 更新 =================

func agentBinaryPath(arch string) string {
	return filepath.Join(agentDirName, binaryPrefix+normalizeArch(arch))
}

func normalizeArch(arch string) string {
	switch strings.ToLower(arch) {
	case "x86_64", "amd64", "x64":
		return "amd64"
	case "aarch64", "arm64", "armv8":
		return "arm64"
	}
	return strings.ToLower(arch)
}

func cachedArchList() []string {
	var list []string
	for _, a := range strings.Split(supportedArch, ",") {
		if _, err := os.Stat(agentBinaryPath(a)); err == nil {
			list = append(list, a)
		}
	}
	return list
}

// startAgentSync 预下载各架构 Agent 二进制到面板本地
func startAgentSync() {
	go func() {
		setUpdateState("获取版本信息", 5)
		rel, err := fetchLatestRelease()
		if err != nil {
			failUpdateState(err.Error())
			return
		}
		os.MkdirAll(agentDirName, 0755)

		total := strings.Split(supportedArch, ",")
		ok := 0
		for i, arch := range total {
			setUpdateState("下载 "+arch+" 客户端", 10+70*i/len(total))
			asset := findAsset(rel, binaryPrefix+arch)
			if asset == nil {
				continue
			}
			dest := agentBinaryPath(arch)
			os.Remove(dest + ".tmp")
			if err := downloadAsset(asset, dest); err != nil {
				continue
			}
			ok++
		}
		if ok == 0 {
			failUpdateState("未下载到任何客户端二进制，请检查网络或更换加速镜像")
			return
		}
		saveConfig("agent_bundle_version", rel.Version)
		globalConfig.Lock()
		globalConfig.AgentBundleVersion = rel.Version
		globalConfig.Unlock()
		doneUpdateState("已同步 " + rel.Version + " 客户端（" + fmt.Sprintf("%d", ok) + " 个架构）")
	}()
}

// pushAgentUpdate 向节点下发更新指令；id 为空或 all 表示全部
func pushAgentUpdate(id string) (int, error) {
	bundle := getAgentBundleVersion()
	if bundle == "" || len(cachedArchList()) == 0 {
		return 0, fmt.Errorf("请先点击「同步最新版本」下载客户端程序")
	}
	if id == "" || id == "all" {
		res := db.Model(&Node{}).Where("denied = ?", false).Update("pending_update", bundle)
		return int(res.RowsAffected), res.Error
	}
	res := db.Model(&Node{}).Where("agent_id = ?", id).Update("pending_update", bundle)
	return int(res.RowsAffected), res.Error
}

// ================= Agent 端自更新 =================

func canRetryUpdate(fails map[string]time.Time, version string) bool {
	t, ok := fails[version]
	if !ok {
		return true
	}
	return time.Since(t) > 10*time.Minute
}

// doAgentUpdate 从面板下载新版本并替换自身（成功后进程退出，由守护进程拉起）
func doAgentUpdate(server, token string, upd UpdateCommand) error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("仅支持 Linux 环境自更新")
	}
	target := selfPath()
	tmp := target + ".update"
	os.Remove(tmp)

	url := strings.TrimRight(server, "/") + upd.URL
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", token)
	req.Header.Set("User-Agent", "HubMonitor-Agent")

	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("下载失败 HTTP %d", resp.StatusCode)
	}

	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), resp.Body)
	f.Close()
	if err != nil {
		os.Remove(tmp)
		return err
	}
	if n == 0 {
		os.Remove(tmp)
		return fmt.Errorf("下载内容为空")
	}
	if upd.SHA256 != "" && !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), upd.SHA256) {
		os.Remove(tmp)
		return fmt.Errorf("校验和不匹配，已放弃更新")
	}
	os.Chmod(tmp, 0755)

	fmt.Println(">> 新版本校验通过，正在替换并重启…")
	if err := applyUpdateAndRestart(tmp, target, "monitor"); err != nil {
		return err
	}
	// 主动退出，交由更新脚本与服务管理器拉起新版本
	time.Sleep(500 * time.Millisecond)
	os.Exit(0)
	return nil
}

// buildUpdateCommand 构造下发给 Agent 的更新指令
func buildUpdateCommand(node Node) *UpdateCommand {
	if node.PendingUpdate == "" {
		return nil
	}
	arch := normalizeArch(node.Arch)
	if arch != "amd64" && arch != "arm64" {
		return nil
	}
	path := agentBinaryPath(arch)
	if _, err := os.Stat(path); err != nil {
		return nil
	}
	sha, err := fileSHA256(path)
	if err != nil {
		return nil
	}
	return &UpdateCommand{
		Version: node.PendingUpdate,
		URL:     "/api/agent/binary?arch=" + arch,
		SHA256:  sha,
	}
}
