# ============================================================
#  nyatmc Makefile
#
#  最常用：
#    make            显示所有目标（同 make help）
#    make build      编译本机二进制 → build/nyatmc
#    make build-all  交叉编译 linux/amd64、linux/arm64、windows/amd64
#    make termux     给 Termux（Android / linux-arm64）用的静态二进制
#    make test vet   跑测试与静态检查
#
#  注意：Windows 上建议在 Git Bash / MSYS2 里执行 make（配方里用到了
#  mkdir / rm / sha256sum 这类命令）；环境里没有 make 也没关系，
#  bash scripts/build.sh all 可以独立完成同样的交叉编译。
#
#  配方行必须用 Tab 缩进，不要用空格。
# ============================================================

# ---------------------------------------------------------------- 变量

BINARY    ?= nyatmc
BUILD_DIR ?= build
GO        ?= go
MODULE    ?= github.com/yuzumikonami/nyatmc
GOFLAGS   ?= -trimpath

# 版本信息：默认取 git 描述，取不到就退回 dev；可用 make VERSION=1.2.3 覆盖。
VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT    ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE      ?= $(shell git log -1 --format=%cI 2>/dev/null || echo unknown)

# 注入 cmd/version.go 里的三个变量
LDFLAGS   := -X $(MODULE)/cmd.Version=$(VERSION) -X $(MODULE)/cmd.Commit=$(COMMIT) -X $(MODULE)/cmd.BuildDate=$(DATE)

# 本机可执行文件后缀（Windows 上是 .exe）
GOEXE     := $(shell $(GO) env GOEXE)

# 校验和工具；macOS 等没有 sha256sum 的环境可覆盖：make checksums SHA256="shasum -a 256"
SHA256    ?= sha256sum

# run 目标透传的参数：make run ARGS="status --all"
ARGS      ?=

# 交叉编译一律关闭 cgo，保证单个二进制、零运行时依赖
export CGO_ENABLED = 0

.DEFAULT_GOAL := help
.PHONY: help build build-all linux-amd64 linux-arm64 windows-amd64 termux \
        test vet fmt tidy clean run install checksums release-snapshot

# ---------------------------------------------------------------- 帮助

help:
	@echo "nyatmc 构建目标："
	@echo ""
	@echo "  make help              显示这份帮助（默认目标）"
	@echo "  make build             编译本机平台二进制到 $(BUILD_DIR)/$(BINARY)$(GOEXE)"
	@echo "  make build-all         交叉编译三个平台（linux/amd64、linux/arm64、windows/amd64）"
	@echo "  make linux-amd64       只编译 linux/amd64"
	@echo "  make linux-arm64       只编译 linux/arm64"
	@echo "  make windows-amd64     只编译 windows/amd64"
	@echo "  make termux            编译 linux/arm64 静态二进制，并提示安装到 \$$PREFIX/bin"
	@echo "  make test              运行全部单元测试"
	@echo "  make vet               运行 go vet"
	@echo "  make fmt               运行 go fmt"
	@echo "  make tidy              运行 go mod tidy"
	@echo "  make run ARGS=...      直接运行（例如 make run ARGS=status）"
	@echo "  make install           安装到 GOBIN（go install）"
	@echo "  make checksums         为 $(BUILD_DIR)/ 下的产物生成 sha256sums.txt"
	@echo "  make release-snapshot  三平台构建 + 校验和（本地发布预演）"
	@echo "  make clean             删除 $(BUILD_DIR)/"
	@echo ""
	@echo "当前版本注入：VERSION=$(VERSION)"
	@echo "              COMMIT=$(COMMIT)"
	@echo "              DATE=$(DATE)"

# ---------------------------------------------------------------- 构建

build:
	@mkdir -p $(BUILD_DIR)
	$(GO) build $(GOFLAGS) -ldflags "-s -w $(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY)$(GOEXE) .
	@echo "已生成 $(BUILD_DIR)/$(BINARY)$(GOEXE)（$(VERSION)）"

build-all: linux-amd64 linux-arm64 windows-amd64
	@echo "三个平台构建完成，产物在 $(BUILD_DIR)/ 下"

linux-amd64:
	@mkdir -p $(BUILD_DIR)
	GOOS=linux GOARCH=amd64 $(GO) build $(GOFLAGS) -ldflags "-s -w $(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY)-linux-amd64 .
	@echo "已生成 $(BUILD_DIR)/$(BINARY)-linux-amd64"

linux-arm64:
	@mkdir -p $(BUILD_DIR)
	GOOS=linux GOARCH=arm64 $(GO) build $(GOFLAGS) -ldflags "-s -w $(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY)-linux-arm64 .
	@echo "已生成 $(BUILD_DIR)/$(BINARY)-linux-arm64"

windows-amd64:
	@mkdir -p $(BUILD_DIR)
	GOOS=windows GOARCH=amd64 $(GO) build $(GOFLAGS) -ldflags "-s -w $(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY)-windows-amd64.exe .
	@echo "已生成 $(BUILD_DIR)/$(BINARY)-windows-amd64.exe"

termux: linux-arm64
	@echo ""
	@echo "Termux 安装（Android 上 arm64 就是本机架构）："
	@echo "  cp $(BUILD_DIR)/$(BINARY)-linux-arm64 \$$PREFIX/bin/$(BINARY)"
	@echo "  chmod +x \$$PREFIX/bin/$(BINARY)"
	@echo "  $(BINARY) version"
	@echo "  也可以直接运行：bash scripts/install.sh --local"

# ---------------------------------------------------------------- 质量

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

fmt:
	$(GO) fmt ./...

tidy:
	$(GO) mod tidy

# ---------------------------------------------------------------- 运行与安装

run:
	$(GO) run . $(ARGS)

install:
	$(GO) install -ldflags "-s -w $(LDFLAGS)" .
	@echo "已安装到 GOBIN（未设置时为 \$$(go env GOPATH)/bin）；Termux 上可用 scripts/install.sh 装进 \$$PREFIX/bin"

# ---------------------------------------------------------------- 清理与发布

clean:
	@rm -rf $(BUILD_DIR)
	@echo "已删除 $(BUILD_DIR)/"

checksums:
	@test -d $(BUILD_DIR) || { echo "还没有 $(BUILD_DIR)/，先执行 make build-all"; exit 1; }
	@cd $(BUILD_DIR) && $(SHA256) $(BINARY)-linux-amd64 $(BINARY)-linux-arm64 $(BINARY)-windows-amd64.exe > sha256sums.txt
	@cat $(BUILD_DIR)/sha256sums.txt

release-snapshot: build-all checksums
	@echo ""
	@echo "本地发布预演完成：$(BUILD_DIR)/ 下有三个平台的二进制与 sha256sums.txt"
	@echo "打 tag（vX.Y.Z）后由 .github/workflows/release.yml 自动发布到 GitHub Releases。"
