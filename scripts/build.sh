#!/usr/bin/env bash
# ============================================================================
#  nyatmc 构建脚本
#
#  Termux 上经常没有 make，这个脚本只依赖 bash + go (+ git)，可以独立完成
#  本机编译与交叉编译；产物统一放在 build/ 下，并打印大小与 sha256。
#
#  用法：
#    bash scripts/build.sh                 编译本机平台 → build/nyatmc
#    bash scripts/build.sh all             三个平台（linux/amd64、linux/arm64、windows/amd64）
#    bash scripts/build.sh linux/arm64     指定任意 GOOS/GOARCH
#    bash scripts/build.sh linux/amd64 windows/amd64
#    bash scripts/build.sh -h              查看帮助
#
#  可用环境变量覆盖：
#    VERSION / COMMIT / DATE   版本信息（默认从 git / 时间推断）
#    BUILD_DIR                 输出目录（默认 build）
#    BINARY                    程序名（默认 nyatmc）
# ============================================================================
set -euo pipefail

BINARY="${BINARY:-nyatmc}"
BUILD_DIR="${BUILD_DIR:-build}"
MODULE="github.com/yuzumikonami/nyatmc"

# ---------------------------------------------------------------- 输出助手

info() { printf '\033[36m==>\033[0m %s\n' "$*"; }
ok()   { printf '\033[32m  ✓\033[0m %s\n' "$*"; }
warn() { printf '\033[33m  ! \033[0m %s\n' "$*" >&2; }
die()  { printf '\033[31m错误：\033[0m %s\n' "$*" >&2; exit 1; }

usage() {
	cat <<'EOF'
nyatmc 构建脚本（不需要 make，Termux 上也能直接用）

用法：
  bash scripts/build.sh [平台...]
  bash scripts/build.sh -h | --help

参数：
  （无参数）     编译当前平台，输出 build/nyatmc（Windows 上是 build/nyatmc.exe）
  all           编译全部三个平台：linux/amd64、linux/arm64、windows/amd64
  GOOS/GOARCH   编译指定平台，例如 linux/amd64、linux/arm64、windows/amd64，
                也可以一次给多个；交叉编译产物名会带平台后缀。

环境变量：
  VERSION    版本号（默认 `git describe --tags --always --dirty`，失败则 dev）
  COMMIT     提交号（默认 `git rev-parse --short HEAD`）
  DATE       构建时间（默认当前 UTC 时间）
  BUILD_DIR  输出目录（默认 build，相对仓库根目录）
  BINARY     程序名（默认 nyatmc）

产物：
  build/nyatmc-<goos>-<goarch>[.exe]   交叉编译产物
  build/nyatmc[.exe]                   本机编译产物
  build/sha256sums.txt                 本次构建的校验和清单
EOF
}

# ---------------------------------------------------------------- 参数

if [ "${1:-}" = "-h" ] || [ "${1:-}" = "--help" ]; then
	usage
	exit 0
fi

# ---------------------------------------------------------------- 环境检查

if ! command -v go >/dev/null 2>&1; then
	die "找不到 go 命令，无法构建。安装方式：
  · Termux：pkg install golang
  · Debian / Ubuntu：sudo apt install golang-go
  · 其它平台：https://go.dev/dl/
  也可以直接下载现成的二进制：https://github.com/yuzumikonami/nyatmc/releases"
fi

# 切到仓库根目录（脚本可能在任意工作目录下被调用）
script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "${script_dir}/.." && pwd)"
cd "${repo_root}"

[ -f go.mod ] || die "在 ${repo_root} 下没有找到 go.mod，请在 nyatmc 源码目录里运行本脚本"

# ---------------------------------------------------------------- 版本信息

VERSION="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
COMMIT="${COMMIT:-$(git rev-parse --short HEAD 2>/dev/null || echo unknown)}"
DATE="${DATE:-$(date -u +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || echo unknown)}"
LDFLAGS="-s -w -X ${MODULE}/cmd.Version=${VERSION} -X ${MODULE}/cmd.Commit=${COMMIT} -X ${MODULE}/cmd.BuildDate=${DATE}"

# go.mod 里声明的 Go 版本 vs 当前 Go（只提醒，不阻断）
required_go="$(awk '$1 == "go" { print $2; exit }' go.mod 2>/dev/null || true)"
have_go="$(go env GOVERSION 2>/dev/null | sed 's/^go//' || true)"
if [ -n "${required_go}" ] && [ -n "${have_go}" ] && command -v sort >/dev/null 2>&1; then
	lowest="$(printf '%s\n%s\n' "${required_go}" "${have_go}" | sort -V 2>/dev/null | head -n1 || true)"
	if [ -n "${lowest}" ] && [ "${lowest}" != "${required_go}" ]; then
		warn "go.mod 要求 Go >= ${required_go}，当前是 ${have_go}，构建可能失败"
	fi
fi

# ---------------------------------------------------------------- 工具函数

human_size() {
	awk -v n="$1" 'BEGIN {
		split("B KB MB GB TB", u, " ")
		i = 1
		while (n >= 1024 && i < 5) { n /= 1024; i++ }
		if (i == 1) printf "%d%s", n, u[i]; else printf "%.1f%s", n, u[i]
	}'
}

sha256_of() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | awk '{print $1}'
	elif command -v shasum >/dev/null 2>&1; then
		shasum -a 256 "$1" | awk '{print $1}'
	elif command -v openssl >/dev/null 2>&1; then
		openssl dgst -sha256 "$1" | awk '{print $NF}'
	else
		echo "（缺少 sha256sum / shasum / openssl，无法计算）"
	fi
}

# report 打印产物大小与 sha256，并追加到 build/sha256sums.txt
report() {
	local out="$1" size hash
	size="$(wc -c <"${out}" | tr -d '[:space:]')"
	hash="$(sha256_of "${out}")"
	printf '     大小  %s\n' "$(human_size "${size}")"
	printf '     校验  %s\n' "${hash}"
	printf '%s  %s\n' "${hash}" "$(basename "${out}")" >>"${BUILD_DIR}/sha256sums.txt"
}

# ---------------------------------------------------------------- 构建

build_host() {
	local goos goarch out
	goos="$(go env GOOS)"
	goarch="$(go env GOARCH)"
	out="${BUILD_DIR}/${BINARY}$(go env GOEXE)"
	info "编译本机平台 ${goos}/${goarch} → ${out}"
	CGO_ENABLED=0 go build -trimpath -ldflags "${LDFLAGS}" -o "${out}" . ||
		die "本机构建失败"
	report "${out}"
	ok "已生成 ${out}"
}

build_one() {
	local goos="$1" goarch="$2" out
	case "${goos}" in
	windows) out="${BUILD_DIR}/${BINARY}-${goos}-${goarch}.exe" ;;
	*) out="${BUILD_DIR}/${BINARY}-${goos}-${goarch}" ;;
	esac
	info "编译 ${goos}/${goarch} → ${out}"
	CGO_ENABLED=0 GOOS="${goos}" GOARCH="${goarch}" \
		go build -trimpath -ldflags "${LDFLAGS}" -o "${out}" . ||
		die "${goos}/${goarch} 构建失败"
	report "${out}"
	ok "已生成 ${out}"
}

# ---------------------------------------------------------------- 主流程

info "nyatmc 构建：版本 ${VERSION}，提交 ${COMMIT}，时间 ${DATE}"
mkdir -p "${BUILD_DIR}"
: >"${BUILD_DIR}/sha256sums.txt"

specs=()
saw_arm64=0
for arg in "$@"; do
	case "${arg}" in
	all) specs+=("linux/amd64" "linux/arm64" "windows/amd64") ;;
	*/*) specs+=("${arg}") ;;
	*) die "无法识别的参数：${arg}（用 GOOS/GOARCH 形式，例如 linux/arm64；或者 all）" ;;
	esac
done

if [ "${#specs[@]}" -eq 0 ]; then
	build_host
else
	for spec in "${specs[@]}"; do
		goos="${spec%%/*}"
		goarch="${spec##*/}"
		if [ -z "${goos}" ] || [ -z "${goarch}" ] || [ "${goos}" = "${spec}" ]; then
			die "平台写法不对：${spec}（应为 GOOS/GOARCH，例如 linux/arm64）"
		fi
		[ "${spec}" = "linux/arm64" ] && saw_arm64=1
		build_one "${goos}" "${goarch}"
	done
fi

printf '\n'
ok "构建完成：${VERSION}（commit ${COMMIT}）"
if [ -s "${BUILD_DIR}/sha256sums.txt" ]; then
	printf '   校验和清单 %s/sha256sums.txt：\n' "${BUILD_DIR}"
	sed 's/^/     /' "${BUILD_DIR}/sha256sums.txt"
fi

if [ "${saw_arm64}" -eq 1 ]; then
	printf '\n'
	info "Termux 安装提示（Android 上 arm64 就是本机架构）："
	printf '     cp %s/%s-linux-arm64 "$PREFIX/bin/%s" && chmod +x "$PREFIX/bin/%s"\n' \
		"${BUILD_DIR}" "${BINARY}" "${BINARY}" "${BINARY}"
	printf '     或者直接运行：bash scripts/install.sh --local\n'
fi
