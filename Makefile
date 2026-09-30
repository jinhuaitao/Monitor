# Hub Monitor —— 本地开发命令
#
# 这里的每条命令都与 .github/workflows/build.yml 里跑的一致。
# 「本地怎么跑」和「CI 怎么跑」写成两套，是质量门禁最常见的失效方式：
# 本地全绿、CI 全红，或者反过来。

GO          ?= go
BINARY      ?= monitor
VERSION     ?= dev
COMMIT      ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_TIME  ?= $(shell date -u +'%Y-%m-%dT%H:%M:%SZ')
LDFLAGS     := -s -w -X main.BuildVersion=$(VERSION) -X main.BuildCommit=$(COMMIT) -X main.BuildTime=$(BUILD_TIME)

.PHONY: all build fmt fmt-check vet test test-race vuln check run clean docker

all: check build

## build: 编译到当前目录（带版本注入）
build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY) .

## fmt: 格式化全部源码
fmt:
	$(GO) fmt ./...

## fmt-check: 校验格式化（有未格式化的文件即失败）
fmt-check:
	@out="$$(gofmt -l .)"; \
	if [ -n "$$out" ]; then \
		echo "以下文件未格式化，请执行 make fmt："; echo "$$out"; exit 1; \
	fi

## vet: 静态检查
vet:
	$(GO) vet ./...

## test: 跑测试
test:
	$(GO) test -count=1 ./...

## test-race: 带竞态检测跑测试（CI 用的就是这个）
test-race:
	$(GO) test -count=1 -race ./...

## vuln: 依赖漏洞扫描
vuln:
	$(GO) run golang.org/x/vuln/cmd/govulncheck@latest ./...

## check: 本地提交前跑一遍，等价于 CI 的 verify job
check: fmt-check vet test-race

## run: 本地启动面板
run:
	$(GO) run . -mode server -port 8080

## docker: 构建容器镜像
docker:
	docker build --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) -t hub-monitor:$(COMMIT) .

clean:
	rm -f $(BINARY) monitor.old monitor.update
	rm -rf dist build

help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/^## /  /'
