package main

// 构建期通过 -ldflags "-X main.BuildVersion=xxx" 注入
// 未注入时默认 dev，面板会提示为本地构建版本
var (
	BuildVersion = "dev"
	BuildCommit  = "unknown"
	BuildTime    = "unknown"
)

// displayVersion 统一展示格式
func displayVersion() string {
	if BuildVersion == "" || BuildVersion == "dev" {
		return "dev"
	}
	return BuildVersion
}
