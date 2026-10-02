#!/usr/bin/env bash
# ============================================================================
#  nyatmc 安装脚本（Termux / Linux）
#
#  默认用 `go install` 拉取并安装最新版本；加 --local 则从当前源码构建后安装。
#
#  安装位置：
#    Termux（Android）  $PREFIX/bin
#    Linux              /usr/local/bin
#    目录不可写         ～/.local/bin（并提示把它加入 PATH）
#
#  用法：
#    bash scripts/install.sh                   安装最新版
#    bash scripts/install.sh --local           从当前源码构建并安装
#    bash scripts/install.sh --prefix ~/bin    安装到指定目录
#    bash scripts/install.sh --version v0.1.0  安装指定 tag / 分支 / 提交
#    bash scripts/install.sh --uninstall       卸载
#    bash scripts/install.sh -h                查看帮助
# ============================================================================
set -euo pipefail

MODULE="github.com/yuzumikonami/nyatmc"
BINARY="nyatmc"

LOCAL=0
UNINSTALL=0
PREFIX_ARG=""
REF="latest"

# ---------------------------------------------------------------- 输出助手

info() { printf '\033[36m==>\033[0m %s\n' "$*"; }
ok()   { printf '\033[32m  ✓\033[0m %s\n' "$*"; }
warn() { printf '\033[33m  ! \033[0m %s\n' "$*" >&2; }
die()  { printf '\033[31m错误：\033[0m %s\n' "$*" >&2; exit 1; }

usage() {
	cat <<'EOF'
nyatmc 安装脚本

用法：
  bash scripts/install.sh [选项]

选项：
  --local              不用 go install，而是用当前源码 `go build` 后安装
  --prefix DIR         安装到指定目录（DIR 不以 /bin 结尾时自动补上 /bin）
  --version REF        安装指定版本，例如 v0.1.0（默认 latest）
  --uninstall          卸载（从常见安装目录里删除 nyatmc）
  -h, --help           显示这份帮助

安装位置：
  Termux（Android）→ $PREFIX/bin
  Linux           → /usr/local/bin
  以上目录没有写权限时，会自动退回到 ~/.local/bin 并提示加入 PATH

安装完成后的常用命令：
  nyatmc init      初始化数据目录与默认实例
  nyatmc start     启动服务器（后台守护）
  nyatmc status    查看状态
EOF
}

# ---------------------------------------------------------------- 参数解析

while [ $# -gt 0 ]; do
	case "$1" in
	--local) LOCAL=1 ;;
	--uninstall | --remove) UNINSTALL=1 ;;
	--prefix)
		shift
		[ $# -gt 0 ] || die "--prefix 后面要跟目录，例如 --prefix ~/bin"
		PREFIX_ARG="$1"
		;;
	--prefix=*) PREFIX_ARG="${1#--prefix=}" ;;
	--version | --ref)
		shift
		[ $# -gt 0 ] || die "--version 后面要跟版本号，例如 --version v0.1.0"
		REF="$1"
		;;
	--version=*) REF="${1#--version=}" ;;
	-h | --help)
		usage
		exit 0
		;;
	*) die "无法识别的参数：$1（用 -h 查看帮助）" ;;
	esac
	shift
done

# 手动展开 --prefix 里的 ~（引号包住的 ~ 不会被 shell 展开）
case "${PREFIX_ARG}" in
"") ;;
"~") PREFIX_ARG="${HOME}" ;;
"~/"*) PREFIX_ARG="${HOME}/${PREFIX_ARG#\~/}" ;;
esac

# ---------------------------------------------------------------- 位置计算

is_termux() {
	if [ -n "${TERMUX_VERSION:-}" ]; then
		return 0
	fi
	case "${PREFIX:-}" in
	*com.termux*) return 0 ;;
	esac
	return 1
}

# resolve_dir 给出首选安装目录。
resolve_dir() {
	if [ -n "${PREFIX_ARG}" ]; then
		case "${PREFIX_ARG}" in
		*/bin) printf '%s' "${PREFIX_ARG}" ;;
		*) printf '%s/bin' "${PREFIX_ARG%/}" ;;
		esac
		return
	fi
	if is_termux && [ -n "${PREFIX:-}" ]; then
		printf '%s/bin' "${PREFIX%/}"
		return
	fi
	printf '/usr/local/bin'
}

ensure_dir() {
	local dir="$1"
	mkdir -p "${dir}" 2>/dev/null || return 1
	[ -w "${dir}" ] || return 1
	return 0
}

# ---------------------------------------------------------------- 卸载

if [ "${UNINSTALL}" -eq 1 ]; then
	candidates=()
	if [ -n "${PREFIX_ARG}" ]; then
		candidates+=("$(resolve_dir)")
	fi
	if [ -n "${PREFIX:-}" ]; then
		candidates+=("${PREFIX%/}/bin")
	fi
	candidates+=("/usr/local/bin" "${HOME}/.local/bin")

	removed=0
	for dir in "${candidates[@]}"; do
		if [ -f "${dir}/${BINARY}" ]; then
			if rm -f "${dir}/${BINARY}" 2>/dev/null; then
				ok "已删除 ${dir}/${BINARY}"
				removed=1
			else
				warn "删除 ${dir}/${BINARY} 失败（权限不足？试试用 sudo 手动删除）"
			fi
		fi
	done
	if [ "${removed}" -eq 0 ]; then
		warn "没有在常见安装目录里找到 ${BINARY}"
	fi
	printf '\n'
	info "配置与数据目录没有动，需要的话请自行删除：rm -rf ~/.nyatmc"
	exit 0
fi

# ---------------------------------------------------------------- 环境检查

if ! command -v go >/dev/null 2>&1; then
	die "找不到 go 命令。安装方式：
  · Termux：pkg install golang
  · Debian / Ubuntu：sudo apt install golang-go
  · 其它平台：https://go.dev/dl/
  或者不用 Go，直接下载现成二进制：https://github.com/yuzumikonami/nyatmc/releases"
fi

PATH_HINT=""
target_dir="$(resolve_dir)"
if ! ensure_dir "${target_dir}"; then
	if [ -n "${PREFIX_ARG}" ]; then
		die "指定的安装目录 ${target_dir} 不可写（用 sudo 运行，或换一个目录）"
	fi
	fallback="${HOME}/.local/bin"
	warn "${target_dir} 不可写，改装到 ${fallback}"
	target_dir="${fallback}"
	ensure_dir "${target_dir}" || die "创建 ${target_dir} 失败"
fi

# 安装目录不在 PATH 里时提醒一句
case ":${PATH}:" in
*":${target_dir}:"*) ;;
*) PATH_HINT="${target_dir}" ;;
esac

# ---------------------------------------------------------------- 取回二进制

tmpdir="$(mktemp -d)"
cleanup() { rm -rf "${tmpdir}"; }
trap cleanup EXIT

if [ "${LOCAL}" -eq 1 ]; then
	script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
	repo_root="$(cd "${script_dir}/.." && pwd)"
	cd "${repo_root}"
	[ -f go.mod ] || die "在 ${repo_root} 下没有找到 go.mod，无法从源码安装（去掉 --local 可改用 go install）"

	VERSION="$(git describe --tags --always --dirty 2>/dev/null || echo dev)"
	COMMIT="$(git rev-parse --short HEAD 2>/dev/null || echo unknown)"
	DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || echo unknown)"
	LDFLAGS="-s -w -X ${MODULE}/cmd.Version=${VERSION} -X ${MODULE}/cmd.Commit=${COMMIT} -X ${MODULE}/cmd.BuildDate=${DATE}"

	info "从当前源码构建 ${BINARY} ${VERSION} → ${target_dir}"
	CGO_ENABLED=0 go build -trimpath -ldflags "${LDFLAGS}" -o "${tmpdir}/${BINARY}" . ||
		die "go build 失败"
else
	info "安装 ${MODULE}@${REF} → ${target_dir}"
	if ! GOBIN="${tmpdir}" go install "${MODULE}@${REF}"; then
		die "go install 失败。可以试试：
  · 用当前源码安装：bash scripts/install.sh --local
  · 直接下载二进制：https://github.com/yuzumikonami/nyatmc/releases"
	fi
fi

[ -f "${tmpdir}/${BINARY}" ] ||
	die "没有找到编译产物 ${tmpdir}/${BINARY}，安装中止"

# ---------------------------------------------------------------- 落地

cp -f "${tmpdir}/${BINARY}" "${target_dir}/${BINARY}" 2>/dev/null ||
	die "写入 ${target_dir}/${BINARY} 失败（权限不足？）"
chmod 0755 "${target_dir}/${BINARY}" 2>/dev/null || true

printf '\n'
ok "安装完成：${target_dir}/${BINARY}"

version_line="$("${target_dir}/${BINARY}" version 2>/dev/null | head -n1 || true)"
if [ -n "${version_line}" ]; then
	printf '   版本      %s\n' "${version_line}"
else
	printf '   版本      运行 %s version 查看（可能需要先配置好 ~/.nyatmc）\n' "${BINARY}"
fi

if [ -n "${PATH_HINT}" ]; then
	printf '\n'
	warn "${PATH_HINT} 不在 PATH 里，先把它加进去："
	printf '     export PATH="%s:$PATH"\n' "${PATH_HINT}"
	printf '     # 想永久生效：把上面这行追加到 ~/.bashrc（zsh 用户是 ~/.zshrc）\n'
fi

printf '\n'
info "后续步骤："
printf '     nyatmc init           初始化数据目录与默认实例\n'
printf '     nyatmc start          启动服务器（首次会自动下载服务端）\n'
printf '     nyatmc status         查看运行状态\n'
