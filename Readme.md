# NyaTMC

**用 Go 编写的 Minecraft 服务器全能管理工具**：编译成单个二进制、零运行时依赖，在 Termux（Android）、Linux 与 Windows 上开箱即用。它是原 Shell 脚本 `AetherCraft` 的进化版，提供 CLI、纯 ANSI 终端看板（TUI）与 Web 仪表盘三种交互方式。

![平台](https://img.shields.io/badge/平台-Termux%20%7C%20Linux%20%7C%20Windows-4c8bf5)
![Go](https://img.shields.io/badge/Go-1.27-00ADD8?logo=go&logoColor=white)
![许可证](https://img.shields.io/badge/许可证-GPL--3.0--or--later-blue)
![状态](https://img.shields.io/badge/状态-活跃开发中-orange)

> **关于这份文档**：内容取自当前源码（`go.mod` 声明 Go 1.27）。
> 命令与参数的唯一真相是 `nyatmc --help` 与 `cmd/*.go`；配置项的唯一真相是
> `internal/config/defaults.go` 里的 `DefaultTOML`。若文档与代码不一致，以代码为准。

---

## 目录

1. [特性一览](#1-特性一览)
2. [快速开始](#2-快速开始)
3. [三种交互方式：CLI / TUI / Web](#3-三种交互方式cli--tui--web)
4. [命令参考](#4-命令参考)
5. [目录结构](#5-目录结构)
6. [配置参考](#6-配置参考)
7. [多实例用法](#7-多实例用法)
8. [日志轮转：为什么不能在线截断 latest.log](#8-日志轮转为什么不能在线截断-latestlog)
9. [常见问题（FAQ）](#9-常见问题faq)
10. [从源码构建与发布](#10-从源码构建与发布)
11. [项目结构与架构](#11-项目结构与架构)
12. [许可证与致谢](#12-许可证与致谢)

---

## 1. 特性一览

| 功能 | 状态 | 说明 |
| --- | --- | --- |
| 核心下载器 | ✅ | `internal/downloader` + `internal/serverjar`：Paper（v3 fill，失败回退 v2）、Fabric、Vanilla（Mojang）、Purpur、Spigot（BuildTools），带 SHA256/SHA1 校验、重试、BMCLAPI 镜像、下载缓存与路径穿越防护。命令：`nyatmc server download/info/check/versions/builds`；`nyatmc start` 在服务端文件缺失且 `server.auto_download = true` 时自动获取（已接线到守护进程） |
| 多实例隔离 | ✅ | `nyatmc create/list/adopt/delete/use/info`，每个实例独立的服务器目录、端口、内存与备份目录 |
| 模组 / 插件管理 | ✅ | `nyatmc mod search/install/list/remove/info`；来源 Modrinth（免注册）或 CurseForge（需 `mods.curseforge_api_key`），可按加载器与 Minecraft 版本过滤，覆盖同名文件前自动备份为 `.bak` |
| 进程守护与自动重启 | ✅ | `nyatmc start` 派生脱离终端的 supervisor；看门狗按 `daemon.max_restarts` / `restart_window` 限制重启风暴 |
| 优雅关闭 | ✅ | 先发 `stop` 指令，超过 `daemon.stop_timeout` 才强杀；`Ctrl+C` 一次优雅、两次强制 |
| 备份 / 回滚 | ✅ | `nyatmc backup`、`backup list`、`backup prune`、`nyatmc restore`；tar.gz/tar/zip，支持 include/exclude、保留份数与最长保留时间 |
| 内置调度器 | ✅ | `nyatmc schedule add/list/remove/enable/disable/run/validate/status/daemon`；cron 表达式 + 动作 backup/restart/stop/start/command |
| 配置热重载 | ✅ | 配置写盘后运行中的 supervisor 与 Web 通过 fsnotify 自动重载（250ms 去抖）；`nyatmc config set` 立即生效 |
| 日志轮转 | ✅ | nyatmc 自身的控制台日志/守护日志按大小轮转压缩；服务端 `logs/latest.log` 采用「归档 + 无人在线时优雅重启切割」策略 |
| 纯 ANSI 看板（TUI） | ✅ | `nyatmc tui`：全屏、单键启停/备份/改配置、多实例切换、实时日志订阅；不引入任何 TUI 库；非 TTY 环境自动退化为表格输出 |
| Web 仪表盘 | ✅ | `nyatmc web serve`：Gin + `go:embed` 原生前端，单二进制内置；令牌鉴权、只读模式、备份/配置/调度/隧道接口 |
| Cloudflare 内网穿透 | ✅ | `nyatmc tunnel start/stop/status/url/install` 调用 cloudflared（自动下载到 `~/.nyatmc/bin`）；`nyatmc web serve --tunnel`（或 `tunnel.auto_start_with_web = true`）随仪表盘一键启动、退出时自动停止；Web 页面的「穿透」按钮可随时启停并显示公网地址 |
| 环境体检 | ✅ | `nyatmc doctor`：数据目录/配置/Java 版本/服务端文件/EULA/端口占用/体积占用/依赖工具，失败时退出码 1 |
| 跨平台单二进制 | ✅ | `CGO_ENABLED=0` 交叉编译 linux/amd64、linux/arm64、windows/amd64，无任何运行时依赖 |

图例：✅ 已实现（上表所有功能均已落地）

---

## 2. 快速开始

### 2.1 安装

**方式一：GitHub Releases（推荐给 Termux / 不想装 Go 的人）**

从 [Releases](https://github.com/yuzumikonami/nyatmc/releases) 下载对应文件，并核对 `sha256sums.txt`：

| 平台 | 文件名 |
| --- | --- |
| Termux / Android（arm64） | `nyatmc-linux-arm64` |
| Linux x86_64 | `nyatmc-linux-amd64` |
| Windows x64 | `nyatmc-windows-amd64.exe` |

```bash
# Termux 示例（linux-arm64）
curl -LO https://github.com/yuzumikonami/nyatmc/releases/latest/download/nyatmc-linux-arm64
curl -LO https://github.com/yuzumikonami/nyatmc/releases/latest/download/sha256sums.txt
sha256sum -c sha256sums.txt --ignore-missing
install -m 0755 nyatmc-linux-arm64 "$PREFIX/bin/nyatmc"
nyatmc version
```

**方式二：`go install`**

```bash
go install github.com/yuzumikonami/nyatmc@latest     # 二进制在 $(go env GOPATH)/bin
```

**方式三：一键脚本（自动挑目录，无写权限时退回 `~/.local/bin`）**

```bash
bash scripts/install.sh                 # go install 最新版
bash scripts/install.sh --local         # 用当前源码构建并安装
bash scripts/install.sh --prefix ~/bin  # 指定目录
bash scripts/install.sh --uninstall     # 卸载
```

**方式四：源码编译**

```bash
git clone https://github.com/yuzumikonami/nyatmc.git
cd nyatmc
make build          # 或： bash scripts/build.sh
```

### 2.2 初始化并第一次启动

```bash
nyatmc init --eula          # 建数据目录 + config.toml + 默认实例（并确认 EULA）
nyatmc doctor               # 体检：Java、端口、EULA、目录权限
nyatmc start                # 后台启动（派生 supervisor）
nyatmc status               # 看状态；脚本可用退出码判断
nyatmc tui                  # 终端看板；或 nyatmc web serve 打开网页
```

`nyatmc init` 会做三件事（源码：`cmd/init.go`）：

1. 创建数据目录（默认 `~/.nyatmc`，可用 `--home` / `NYATMC_HOME` 覆盖）；
2. 生成带中文注释的 `config.toml`（已存在则不动）；
3. 创建默认实例（默认名 `default`），写好 `server.properties`、`eula.txt`、`start.sh`/`start.bat`。

> 💡 **服务端 jar 会自动获取**：`nyatmc start` 发现文件缺失时（`server.auto_download = true`，缺省开启）
> 会按 `server.type` / `server.minecraft_version` 自动下载并校验哈希，无需手工准备。
> 也可以先手动执行：`nyatmc server download [实例名]`、`nyatmc server check`、`nyatmc server versions paper`。

### 2.3 Termux 完整命令序列

```bash
# 1) 准备环境（Termux 上必须有 Java 才能跑服务端）
pkg update && pkg upgrade
pkg install openjdk-21          # 1.20.5+ 需要 Java 21；1.18~1.20.4 装 openjdk-17
termux-wake-lock                # 关键：防止 Android 在后台杀掉进程

# 2) 安装 nyatmc（两种任选其一）
pkg install golang && go install github.com/yuzumikonami/nyatmc@latest
#   或者直接下载 release 二进制：
#   curl -LO .../nyatmc-linux-arm64 && install -m 0755 nyatmc-linux-arm64 "$PREFIX/bin/nyatmc"

# 3) 初始化并启动
nyatmc init --eula
nyatmc doctor
nyatmc start --wait
nyatmc status
nyatmc tui                      # 需要真正的终端；脚本里请用 nyatmc status --json
```

在 Termux 里 `make` 通常不存在，所以交叉编译请用脚本（`bash scripts/build.sh all`），
安装请用 `bash scripts/install.sh`（它会装到 `$PREFIX/bin`）。

---

## 3. 三种交互方式：CLI / TUI / Web

### 3.1 CLI（脚本 / cron 友好）

```bash
nyatmc start survival --wait          # 等到就绪
nyatmc status --json                  # 机器可读
nyatmc command "say 5 分钟后维护"      # 直接发服务端指令
nyatmc backup survival --label nightly
nyatmc logs -f --grep ERROR
```

所有面向脚本的命令都遵循「成功退出码 0，失败非 0」，`status` 更有专门约定（见 [4.1](#41-全局标志与退出码)）。

### 3.2 TUI：终端看板（`nyatmc tui`）

全屏纯 ANSI 渲染，不依赖任何 TUI 库。所有按键**单键生效，不需要回车**。

**看板模式**（源码：`internal/tui/keys.go`）

| 按键 | 作用 |
| --- | --- |
| `s` / `S` | 启动实例（后台守护 + 看门狗） |
| `e` / `E` | 优雅停止（发 `stop` 指令，超时后强杀） |
| `k` / `K` | 强制结束（等价 `--force`，世界数据可能未完整落盘） |
| `r` / `R` | 重启 |
| `b` / `B` | 立即备份（用 `backup.*` 配置，标签 `manual`，并按保留策略清理） |
| `c` / `C` | 进入配置模式（就地修改 `config.toml` 关键项） |
| `l` / `L` | 只看日志（再按一次恢复完整布局） |
| `p` / `P` | 只看玩家（再按一次恢复完整布局） |
| `f` / `F`、`空格` | 回到日志末尾（跟随最新） |
| `Tab` / `Shift+Tab` | 切换到下一个 / 上一个实例 |
| `1` … `9` | 直接切到第 N 个实例 |
| `↑` / `↓` | 日志上翻 / 下翻一行 |
| `PgUp` / `PgDn` | 翻页 |
| `Home` / `End` | 跳到最早 / 回到末尾 |
| `?` / `h` | 帮助浮层（浮层里按 `q` 退出程序，其它键返回看板） |
| `q` / `Q`、`Ctrl+C` | 退出看板（**不会**停止服务端） |

**配置模式**

| 按键 | 作用 |
| --- | --- |
| `↑` `↓` `PgUp` `PgDn` `Home` `End` | 移动选中项 |
| `Enter` 或 `e` | 编辑当前项（编辑中：`Enter` 提交、`Esc` 取消、`←/→/Home/End` 移动光标、`Backspace/Delete` 删除） |
| `空格` 或 `x` | 切换布尔项 |
| `[` `]` 或 `Tab` / `Shift+Tab` | 循环切换枚举值 |
| `Esc` | 返回看板 |
| `q` | 退出程序（非编辑状态） |

保存时会先读盘、只改你编辑的那一项、整体校验通过后才原子写回，运行中的守护进程与 Web 会自动热重载。

**命令行参数**

| 参数 | 默认 | 说明 |
| --- | --- | --- |
| `--refresh` | `1s` | 刷新间隔；小于 100ms 会被提升到 100ms 以免占满 CPU |
| `--no-alt-screen` | `false` | 不使用备用屏幕缓冲区（调试用：原地刷新） |
| `--instances` | 空 | 只监控这些实例，逗号分隔（例如 `a,b`；全角逗号也认） |

非交互环境（重定向、cron、日志采集）下 `tui` 不会喷转义码，而是打印一份实例状态表格并提示改用
`nyatmc status --json` / `nyatmc logs -f` / `nyatmc start -f`。

### 3.3 Web 仪表盘（`nyatmc web serve`）

```bash
nyatmc web serve                      # 默认 http://127.0.0.1:8080/
nyatmc web serve --listen 0.0.0.0:8080 --token <令牌>   # 对外暴露必须带令牌
nyatmc web token --save               # 生成随机令牌并写进配置
```

| 参数 | 默认 | 说明 |
| --- | --- | --- |
| `--listen` | 取 `web.listen`（默认 `127.0.0.1:8080`） | 监听地址 |
| `--token` | 取 `web.token` | 本次运行的访问令牌，不写入配置 |
| `--open` | `false` | 启动后自动打开浏览器（Termux 下走 `termux-open-url`） |
| `--tunnel` | `false` | 同时启动 Cloudflare 穿透，把仪表盘暴露到公网；已指向相同目标的隧道会被复用，退出 web serve 时自动停止（等价于 `tunnel.auto_start_with_web = true`） |

`nyatmc web token [--save]`：生成 32 字节十六进制随机令牌；`--save` 会写入 `[web].token`
（注意：保存会用 TOML 序列化重写整个配置文件，**原有注释会丢失**）。

**安全模型（务必读）**

* 默认只监听回环地址 `127.0.0.1`，本机以外访问不到；
* 只监听回环时可以不设令牌；一旦监听地址不是回环，而 `web.token` 为空，
  配置校验的 `Warnings()` 会明确警告「任何人都能控制你的服务器」，`web serve` 启动时也会再警告一次；
* 带令牌访问：`http://127.0.0.1:8080/?token=<令牌>`（令牌会记在浏览器本地）；
* `web.enable_write = false` 时所有写操作接口返回 403，页面进入只读模式；
* 前端与接口都 `go:embed` 进二进制，不依赖 CDN，离线/内网可用；
* 要放到公网，推荐用穿透而不是直接监听 `0.0.0.0`：`nyatmc tunnel start --port 8080`。

---

## 4. 命令参考

> 下表所有命令、别名、参数均逐条取自 `cmd/*.go`，可在本机用 `nyatmc <命令> --help` 复核。
> Cobra 自带 `nyatmc completion bash|zsh|fish|powershell` 用于生成补全脚本。

### 4.1 全局标志与退出码

| 标志 | 说明 |
| --- | --- |
| `--config FILE` | 配置文件路径（默认 `~/.nyatmc/config.toml`） |
| `-i, --instance NAME` | 目标实例名（默认取配置里的 `general.default_instance`） |
| `--home DIR` | 数据目录（默认 `~/.nyatmc`，也可用环境变量 `NYATMC_HOME`） |
| `-v, --verbose` | 输出调试日志 |
| `--json` | 以 JSON 输出（大多数命令支持，便于脚本处理） |
| `--no-color` | 关闭彩色输出 |

相关环境变量：`NYATMC_HOME`（数据目录）、`NYATMC_CONFIG`（配置文件路径）。

退出码约定：

| 退出码 | 出现场景 |
| --- | --- |
| `0` | 成功 |
| `1` | 一般错误；`doctor` 发现失败项；`config validate` 校验失败；`schedule validate` 有非法任务 |
| `3` | `status` 实例未在运行；`tunnel status/url` 隧道未在运行；`schedule status` 调度器未在运行 |
| `4` | `status` 实例处于崩溃状态 |

### 4.2 初始化与实例管理

| 命令 | 说明 | 主要参数 |
| --- | --- | --- |
| `nyatmc init [数据目录]` | 初始化数据目录、配置文件与默认实例 | `--force` `--eula` `--type` `--version` `--memory` `--port` `--name` `--java` `--no-instance` |
| `nyatmc create <实例名>` | 创建实例（多实例隔离；`--path` 可接管已有目录） | `--type` `--version` `--build` `--loader-version` `--installer-version` `--memory` `--port` `--path` `--description` `--eula` `--java` `--no-download` `--start` |
| `nyatmc list`（别名 `ls`） | 列出所有实例及其状态（`*` 表示默认实例） | `--json` |
| `nyatmc adopt <实例名>` | 把已有服务器目录纳管为实例（不移动、不删除文件） | `--path` |
| `nyatmc delete <实例名>`（别名 `rm`、`remove`） | 删除实例：默认只注销，`--purge` 连文件一起删 | `--purge` `--keep-backups` `-y, --yes` |
| `nyatmc use <实例名>` | 设置默认实例 | — |
| `nyatmc info [实例名]` | 详细信息：配置、目录、备份、服务端文件 | — |
| `nyatmc doctor` | 环境体检（Java/端口/EULA/权限/体积/工具） | — |

字段说明（`cmd/init.go`）：`--force` 在实例已存在时用当前配置刷新其基础文件；`--no-instance` 只写配置文件；
`create --no-download` 目前没有实际作用（创建过程本来就不下载；服务端文件由首次启动时的
`server.auto_download` 或 `nyatmc server download` 获取）；
`delete --purge` 会二次确认（`-y` 跳过），自定义 `--path` 的服务器目录**永远**不会被自动删除。

### 4.3 进程控制

| 命令 | 说明 | 主要参数 |
| --- | --- | --- |
| `nyatmc start [实例名]` | 启动实例（默认后台守护 + 崩溃自动重启） | `-f, --foreground` `--wait` `--timeout` `--watchdog` `--no-watchdog` `--no-start` `--all` |
| `nyatmc stop [实例名]` | 优雅停止（发 `stop`，超时后强杀，最后停守护进程） | `-f, --force` `--timeout` `--keep-supervisor` `--all` |
| `nyatmc restart [实例名]` | 重启（未运行时直接启动，方便「确保在运行」） | `-f, --force` `--delay` `--wait` `--timeout` `--all` |
| `nyatmc status [实例名]` | 状态：PID、运行时长、版本、内存、TPS、在线玩家 | `--all` `-w, --watch` `-n, --lines` |

| 参数 | 默认 | 说明 |
| --- | --- | --- |
| `start -f, --foreground` | `false` | 守护进程跑在当前终端：服务端输出直接显示，键盘输入即服务端指令，`Ctrl+C` 优雅关闭 |
| `start --wait` | `false` | 等到服务端就绪（打印过 `Done`）或失败再返回 |
| `start --timeout` | `0`（取 `daemon.startup_timeout`） | 配合 `--wait` 的等待上限 |
| `start --watchdog` / `--no-watchdog` | 不覆盖配置 | 本次启动强制开/关崩溃自动重启（两者不能同时给） |
| `start --no-start` | `false` | 只启动守护进程，不拉起服务端 |
| `stop --timeout` | `0`（取 `daemon.stop_timeout`） | 优雅关闭等待上限 |
| `stop --keep-supervisor` | `false` | 只停服务端、保留守护进程；之后可用 `nyatmc restart` 再拉起来 |
| `restart --delay` | `0`（实际 2s） | 停止后等待多久再启动 |
| `status -w, --watch` | `false` | 每 2 秒刷新一次（`Ctrl+C` 退出） |
| `status -n, --lines` | `0` | 额外附带最近 N 行日志 |

内部命令（不出现在帮助里，由程序自己派生）：`nyatmc __supervise`
（`--no-start` / `--attach` / `--watchdog`）、`nyatmc __schedule`。

### 4.4 控制台与指令

| 命令 | 说明 | 主要参数 |
| --- | --- | --- |
| `nyatmc console [实例名]` | 连接服务端控制台：实时日志 + 直接输入指令（`Ctrl+C` 退出） | `-n, --lines`（默认 50，连接时回显的最近行数） |
| `nyatmc command <指令…>`（别名 `cmd`、`send`） | 向运行中的服务端发一条控制台指令，适合脚本 | — |
| `nyatmc say <消息…>` | 在服务器里广播一条消息 | — |

### 4.5 服务端文件管理

| 命令 | 说明 | 主要参数 |
| --- | --- | --- |
| `nyatmc server download [实例名]`（别名 `install`、`get`） | 下载或更新服务端文件 | `--type` `--version` `--build` `--loader-version` `--installer-version` `--mirror` `--force` `--update` `--no-verify` |
| `nyatmc server info [实例名]` | 查看本地已安装的服务端文件（含校验和与安装时间） | — |
| `nyatmc server check [实例名]` | 只查询远端最新版本、不下载 | `--type` `--version` `--build` `--loader-version` `--installer-version` `--mirror` |
| `nyatmc server versions [类型]` | 列出可用的 Minecraft 版本 | `--kind` `--mirror` |
| `nyatmc server builds [类型] [Minecraft 版本]` | 列出可用的构建号（Paper / Purpur） | `--version` |

支持的类型：`paper`、`fabric`、`vanilla`、`purpur`，以及 `spigot`（走官方 BuildTools，需要本机 JDK 与联网拉取 Maven 依赖）。
`--mirror https://bmclapi2.bangbang24.com` 这类国内镜像可加速 Vanilla/Paper；校验和默认开启，`--no-verify` 不推荐。

```bash
nyatmc server versions paper
nyatmc server download survival --version 1.21.4 --build latest
nyatmc server check survival            # 只看看远端有什么新版本
nyatmc server download survival --update   # 升级到最新构建
```

### 4.6 模组 / 插件

| 命令 | 说明 | 主要参数 |
| --- | --- | --- |
| `nyatmc mod search <关键词…>`（别名 `find`） | 搜索模组 / 插件 | `-n, --limit` `--loader` `--version` `--type` |
| `nyatmc mod install <关键词或项目ID…>`（别名 `add`） | 安装到实例（同名文件先备份为 `.bak`） | `--loader` `--version` `-y, --yes` `--no-backup`（已废弃） |
| `nyatmc mod list [实例名]`（别名 `ls`） | 列出已安装的模组 / 插件 | — |
| `nyatmc mod remove <文件名> [实例名]`（别名 `rm`、`uninstall`、`delete`） | 删除已安装文件 | `-y, --yes` |
| `nyatmc mod info <项目ID>` | 查看某项目可安装的版本文件 | `--loader` `--version` |

`mod` 组的持久标志：`--provider modrinth|curseforge`（默认取 `mods.provider`）、`--plugin`（放进 `plugins/`）、
`--mod`（放进 `mods/`）、`--dir`（自定义落盘目录）。CurseForge 需要先在配置里填 `mods.curseforge_api_key`。

### 4.7 日志

| 命令 | 说明 | 主要参数 |
| --- | --- | --- |
| `nyatmc logs [实例名]` | 查看日志：跟随、过滤、手动切割与清理 | `-n, --lines`(200) `-f, --follow` `--file` `--grep` `--rotate` `--truncate` `--prune` `--all` |

```bash
nyatmc logs -n 100                  # 最近 100 行（默认展示 nyatmc 捕获的控制台日志）
nyatmc logs -f                      # 实时跟随，等价 tail -f
nyatmc logs --file latest           # 改看服务端自己的 logs/latest.log
nyatmc logs --grep ERROR -n 50      # 只看包含 ERROR 的行
nyatmc logs --rotate                # 手动归档一份（服务器已停止时顺带清空）
nyatmc logs --truncate              # 配合 --rotate：归档后清空（仅服务端已停止时有效）
nyatmc logs --prune                 # 按 log.keep_files / log.max_age_days 清理历史日志
```

### 4.8 备份与回滚

| 命令 | 说明 | 主要参数 |
| --- | --- | --- |
| `nyatmc backup [实例名]` | 立即备份（打包 include 清单，可选自动清理旧备份） | `--label` `--keep` `--no-prune` `--all` |
| `nyatmc backup list [实例名]` | 列出已有备份 | `--all` |
| `nyatmc backup prune [实例名]` | 按 `backup.keep` / `backup.max_age` 清理 | `--keep` `--all` |
| `nyatmc restore [实例名] [备份名\|latest]` | 回滚到某个备份（默认回滚前先自动备份当前状态） | `-y, --yes` `--target` `--no-backup` `--no-restart` |

备份文件命名：`<实例>-<标签>-<YYYYMMDD-HHMMSS>.<格式>`，例如 `survival-manual-20261001-042400.tar.gz`；
标签默认 `manual`，守护进程自动备份用 `before-start`，回滚前自动备份用 `before-restore`。
回滚会拒绝备份内的绝对路径与 `..` 穿越，并跳过符号链接，只允许写入服务器目录内。

### 4.9 配置管理

| 命令 | 说明 | 主要参数 |
| --- | --- | --- |
| `nyatmc config show` | 显示配置文件（默认原文） | `--raw`（默认 true） |
| `nyatmc config list [前缀]`（别名 `ls`） | 以 `键 = 值` 列出全部配置项 | — |
| `nyatmc config get <键> [实例名]` | 读取配置项；给了实例名则读**合并后**的生效值 | — |
| `nyatmc config set <键> <值> [实例名]` | 修改配置项；给了实例名则只覆盖该实例 | — |
| `nyatmc config unset <键> [实例名]` | 恢复默认（实例覆盖则改为继承全局值） | — |
| `nyatmc config path` | 打印配置文件路径 | — |
| `nyatmc config validate` | 校验配置文件 | — |
| `nyatmc config edit` | 用编辑器打开（保存后自动校验） | `--editor`（默认读 `$VISUAL`/`$EDITOR`） |

`config` 组还有一个持久标志 `--for-instance NAME`，等价于在命令末尾写实例名：

```bash
nyatmc config set server.memory 4G                 # 改全局默认
nyatmc config set server.port 25566 survival       # 只改 survival 实例
nyatmc config set server.port 25566 --for-instance survival
nyatmc config get server.port survival             # 看 survival 的生效值
nyatmc config list server                          # 只看 server.* 段
```

### 4.10 内置调度器

| 命令 | 说明 | 主要参数 |
| --- | --- | --- |
| `nyatmc schedule add <名字> <cron> <动作>` | 添加任务并写入 `[[scheduler.jobs]]` | `--instance` `--command` `--label` `--disabled` |
| `nyatmc schedule list` | 列出全部任务与执行情况 | — |
| `nyatmc schedule remove <名字>` | 删除任务 | — |
| `nyatmc schedule enable/disable <名字>` | 启用 / 停用任务 | — |
| `nyatmc schedule run <名字>` | 立刻手动执行一次（即使已停用） | — |
| `nyatmc schedule validate` | 逐条校验任务（名字、cron、动作、实例名、指令） | — |
| `nyatmc schedule status` | 调度器是否在运行 + 各任务情况（依据 `state/scheduler.json` 的 pid） | — |
| `nyatmc schedule daemon` | 前台运行调度器 | `--detach`（后台派生，脱离终端） `--instances`（只跑这些实例的任务） |

动作（`action`）取值：`backup` / `restart` / `stop` / `start` / `command`（`command` 时必须给 `--command`）。

```bash
nyatmc schedule add daily-backup "0 4 * * *" backup --instance survival --label auto
nyatmc schedule add weekly-restart "@weekly" restart
nyatmc schedule daemon --detach
nyatmc schedule status
```

### 4.9 TUI / Web / 穿透

| 命令 | 说明 | 主要参数 |
| --- | --- | --- |
| `nyatmc tui [实例名]` | 终端看板 | `--refresh`(1s) `--no-alt-screen` `--instances` |
| `nyatmc web serve` | 启动 Web 仪表盘（`Ctrl+C` 优雅退出） | `--listen` `--token` `--open` `--tunnel` |
| `nyatmc web token` | 生成随机访问令牌 | `--save` |
| `nyatmc tunnel start` | 启动 cloudflared 隧道并打印公网地址 | `--port` `--target` `--protocol` `--token` `--hostname` `--bin` `--dir` `--no-install` `--wait`(60s) |
| `nyatmc tunnel stop` | 停止由 nyatmc 启动的隧道并清理状态 | — |
| `nyatmc tunnel status` | 隧道状态与最近 cloudflared 输出 | `-n, --lines`(10) |
| `nyatmc tunnel url` | 只输出公网地址（脚本/扫码用；未就绪时退出码 3） | — |
| `nyatmc tunnel install` | 下载 cloudflared 并报告版本与路径 | `--dir`（默认 `~/.nyatmc/bin`） |

### 4.10 其它

| 命令 | 说明 |
| --- | --- |
| `nyatmc version` | 版本、提交、构建时间、运行环境、数据目录与配置文件（支持 `--json`） |
| `nyatmc`（不带子命令） | 打印帮助 + 实例概览表 |
| `nyatmc completion <shell>` | Cobra 生成的补全脚本 |

---

## 5. 目录结构

### 5.1 数据目录 `~/.nyatmc/`

```text
~/.nyatmc/
├── config.toml                 # 唯一配置入口（TOML，带中文注释，可热重载）
├── config.toml.bak             # 每次写盘前的上一版备份（Config.SaveTo 自动留）
├── state/                      # 运行期状态（调度器等）
│   ├── scheduler.json          # 调度器 pid、各任务最近执行结果
│   └── scheduler.log           # 调度器自身日志
├── cache/                      # 下载缓存（downloader.cache_dir 默认指向这里）
├── backups-keep/               # delete --purge --keep-backups 时转移过来的备份
└── instances/
    └── <实例名>/
        ├── .nyatmc/            # nyatmc 私有元数据（服务器目录保持干净）
        │   ├── instance.json   # 实例静态信息：类型、版本、jar 校验和、创建时间
        │   ├── state.json      # 最近一次运行状态快照（supervisor 不在时也能看）
        │   ├── endpoint.json   # 控制通道地址 + 随机令牌（权限 0600）
        │   ├── supervisor.pid  # 守护进程 pid
        │   ├── supervisor.log  # 守护进程日志（按大小轮转）
        │   └── console.log     # nyatmc 捕获的服务端控制台输出（按大小轮转）
        ├── backups/            # 备份归档（backup.dir 留空时用这里）
        └── server/             # Minecraft 服务端工作目录
            ├── server.jar      # 服务端主 jar（名字由 server.jar_name 决定）
            ├── server.properties, eula.txt, ops.json, whitelist.json …
            ├── start.sh / start.bat   # nyatmc 生成的排查用启动脚本
            ├── log4j2.xml      # 仅在 log.manage_log4j2 = true 时托管（原文件先备份为 .bak）
            ├── world/, world_nether/, world_the_end/
            ├── mods/, plugins/, config/
            └── logs/
                ├── latest.log          # 服务端自己写的日志（JVM 持有句柄）
                └── nyatmc-archive/     # nyatmc 归档的历史日志（压缩后按策略清理）
```

实例的服务器目录可以用 `[instances.<名字>] path` 指到任意位置（例如你已有的服务器目录），
此时 `.nyatmc/` 与 `backups/` 仍留在 `~/.nyatmc/instances/<名字>/` 下。

### 5.2 源码目录树

```text
NyaTMC/
├── main.go                     # 入口：cmd.Execute()
├── go.mod / go.sum             # module github.com/yuzumikonami/nyatmc，go 1.27
├── Makefile                    # 本机构建、三平台交叉编译、校验和、发布预演
├── README.md                   # 本文档
├── LICENSE                     # GPL-3.0-or-later
├── .gitignore
├── .github/workflows/release.yml   # tag 触发的发布流水线
├── scripts/
│   ├── build.sh                # 不依赖 make 的构建脚本（Termux 友好）
│   └── install.sh              # 一键安装 / 卸载（$PREFIX/bin、/usr/local/bin、~/.local/bin）
├── embed/                      # 随二进制分发的模板资源（package assets）
│   ├── config.toml             # 与 internal/config.DefaultTOML 逐字一致
│   ├── README.md
│   ├── assets.go               # go:embed + ConfigTemplate() / ReadmeText()
│   └── assets_test.go          # 断言模板可解析、且与 DefaultTOML 完全一致
├── cmd/                        # CLI（每个文件一组命令）
│   ├── root.go                 # 全局标志、App 上下文、表格/JSON 输出、退出码
│   ├── init.go                 # init
│   ├── instance.go             # create / list / adopt / delete / use / info
│   ├── start.go                # start / stop
│   ├── restart.go              # restart
│   ├── status.go               # status
│   ├── console.go              # console / command / say
│   ├── logs.go                 # logs
│   ├── backup.go               # backup / backup list / backup prune
│   ├── restore.go              # restore
│   ├── config_cmd.go           # config show/list/get/set/unset/path/validate/edit
│   ├── schedule.go             # schedule …（含隐藏的 __schedule）
│   ├── tui.go                  # tui
│   ├── web.go                  # web serve / web token
│   ├── tunnel.go               # tunnel start/stop/status/url/install
│   ├── doctor.go               # doctor
│   ├── supervise.go            # 隐藏命令 __supervise
│   ├── version.go              # version（Version/Commit/BuildDate 由 ldflags 注入）
│   └── util.go                 # 交互确认、tail 读取等
├── internal/
│   ├── config/                 # 配置定义、默认值模板、校验、实例覆盖合并、热重载
│   ├── instance/               # 实例磁盘布局、创建/删除、server.properties、eula、log4j2
│   ├── daemon/                 # supervisor、进程包装、看门狗、状态机、日志轮转
│   ├── logwatch/               # 从服务端输出解析就绪/玩家/TPS/错误
│   ├── backup/                 # tar.gz/tar/zip 打包、回滚、保留策略
│   ├── downloader/             # 服务端下载（Paper/Fabric/Vanilla/Purpur/Spigot + 校验和）
│   ├── mods/                   # Modrinth / CurseForge 模组与插件
│   ├── scheduler/              # 内置 cron 调度器（含守护进程）
│   ├── tunnel/                 # cloudflared 穿透
│   ├── tui/                    # 纯 ANSI 看板
│   └── web/                    # Gin REST API + go:embed 前端
└── pkg/
    ├── logger/                 # 分级日志 + 按大小轮转压缩
    └── socket/                 # 回环 TCP + 随机令牌的本地控制通道（IPC）
```

---

## 6. 配置参考

配置文件：`~/.nyatmc/config.toml`（首次运行自动生成；模板与 `internal/config/defaults.go` 的
`DefaultTOML` 逐字一致，`embed/config.toml` 是它的副本并由单元测试守护一致性）。

> ⚠️ 解析使用 `DisallowUnknownFields`：**写错键名会直接报错**（不是被忽略）。改完保存即生效，运行中的实例会热重载。

### 6.1 值类型语义

| 类型 | 写法 | 说明 |
| --- | --- | --- |
| `Duration` | `"10s"`、`"5m"`、`"1h30m"`、`"2d"`、纯数字（按秒） | `2d` 这种天数是 nyatmc 额外支持的写法 |
| `Size` | `"512KB"`、`"20MB"`、`"1GiB"`、纯数字（字节） | **一律按 1024 进制**解释：`MB` = MiB，与 JVM `-Xmx2G` 语义一致，避免「写 20MB 实际 19MiB」的困惑 |
| `*bool` 开关 | `true` / `false` | 缺省值由访问器给出（见下表），所以「不写」与「显式写」可能不同；`server.memory`、`port` 等字符串/数字不写时用模板默认 |
| 内存字符串 | `"2G"`、`"2048M"` | `server.memory` / `server.min_memory` 只接受 `\d+[KMG]` 形式（大小写均可） |

### 6.2 `[general]`

| 键 | 类型 | 模板默认 | 含义 |
| --- | --- | --- | --- |
| `default_instance` | string | `"default"` | 未显式指定实例时用哪个实例 |
| `language` | string | `"zh-CN"` | 界面语言标记（**预留，当前未生效**） |
| `color` | bool | `true` | 命令行是否彩色输出（普通 bool，删掉该键等价于 `false`） |
| `auto_start` | bool | `false` | supervisor 启动时是否顺带拉起服务端（**预留，当前未生效**：是否拉起服务端由 `nyatmc start` 与 `--no-start` 决定） |
| `auto_update_check` | bool | `false` | 启动时检查新版本（**预留，未在模板中出现**） |

### 6.3 `[server]`

| 键 | 类型 | 模板默认 | 含义 |
| --- | --- | --- | --- |
| `type` | string | `"paper"` | `paper` / `fabric` / `vanilla` / `spigot` / `purpur` / `custom`（非法值会被 `config validate` 拒绝） |
| `minecraft_version` | string | `"latest"` | `latest` 表示自动取最新正式版 |
| `build` | string | `""` | Paper/Purpur 构建号，空表示最新 |
| `loader_version` | string | `""` | Fabric loader 版本，空表示最新 |
| `installer_version` | string | `""` | Fabric installer 版本，空表示最新 |
| `java_path` | string | `"java"` | java 可执行文件（可写绝对路径） |
| `memory` | string | `"2G"` | 最大堆 → `-Xmx` |
| `min_memory` | string | `"512M"` | 最小堆 → `-Xms` |
| `jvm_args` | []string | Aikar 参数裁剪版 | 追加的 JVM 参数（模板里的列表比 `DefaultJVMArgs` 少两条 `-D…aikars…` 统计参数） |
| `server_args` | []string | `["--nogui"]` | 传给服务端的参数 |
| `port` | int | `25565` | 写入 `server.properties` 的 `server-port` |
| `max_players` | int | `20` | `max-players` |
| `motd` | string | `"A NyaTMC powered Minecraft Server"` | `motd` |
| `view_distance` | int | `10` | `view-distance` |
| `online_mode` | `*bool` | **缺省 true** | 正版验证 → `online-mode` |
| `jar_name` | string | `"server.jar"` | 主 jar 文件名（不能含路径分隔符） |
| `world_name` | string | `"world"` | 世界目录名（`level-name`，也用于备份说明） |
| `eula` | `*bool` | **缺省 false** | 是否同意 Minecraft EULA；false 时写出 `eula=false` 并提醒，服务端会拒绝启动 |
| `auto_download` | `*bool` | **缺省 true** | 启动前自动补齐缺失的服务端文件（按 `server.type` / `server.minecraft_version` 下载并校验哈希） |
| `[server.properties]` | map | 空 | 直接覆盖 `server.properties` 的同名键（用户值优先级最高），例如 `enable-rcon = "true"` |

`nyatmc create/init` 还会写入若干基础键：`server-port`、`motd`、`max-players`、`view-distance`、`online-mode`、
`level-name`、`enable-command-block`、`enable-status`、`spawn-protection=16`、`difficulty=easy`、`gamemode=survival`、
`allow-flight=false`、`white-list=false`、`enforce-whitelist=false`、`sync-chunk-writes=true`、`max-tick-time=-1`。
更新 `server.properties` 是**就地替换已有键、追加缺失键**，注释与顺序保留；改完端口后可用 `nyatmc init --force` 重新套用。

### 6.4 `[daemon]`

| 键 | 类型 | 模板默认 | 含义 |
| --- | --- | --- | --- |
| `watchdog` | `*bool` | **缺省 true** | 崩溃自动重启 |
| `max_restarts` | int | `5` | `restart_window` 内最多重启次数，`0` 表示不限 |
| `restart_window` | Duration | `"10m"` | 重启次数统计窗口（实际执行时窗口为 0 会退化为 1h） |
| `restart_delay` | Duration | `"5s"` | 崩溃/退出后等待多久再重启 |
| `stop_command` | string | `"stop"` | 优雅关闭时发往控制台的指令 |
| `stop_timeout` | Duration | `"60s"` | 等待服务端自行退出的上限，超时强杀（必须 > 0） |
| `startup_timeout` | Duration | `"5m"` | 等待「就绪」（日志出现 `Done`）的上限，超时只提醒不干预（必须 > 0） |
| `kill_timeout` | Duration | `"15s"` | 强杀后的等待时间 |
| `capture_console` | `*bool` | **缺省 true** | 是否把服务端输出同时写进 `console.log` |
| `backup_before_start` | `*bool` | **缺省 false** | 每次启动前自动备份（标签 `before-start`） |

看门狗是「窗口内计数」：`restart_window` 内重启次数达到 `max_restarts` 就放弃并置为 `crashed`，
需要人工 `nyatmc start`（也能避免崩溃循环把机器拖垮）。

### 6.5 `[backup]`

| 键 | 类型 | 模板默认 | 含义 |
| --- | --- | --- | --- |
| `dir` | string | `""` | 备份目录；留空用 `<实例目录>/backups` |
| `keep` | int | `10` | 保留份数，`0` 表示不按份数清理（**注意：实例级覆盖时写 0 不会生效**，合并逻辑只在非 0 时覆盖） |
| `max_age` | Duration | `"0s"` | 最长保留时间，`0s` 表示不按时间清理 |
| `format` | string | `"tar.gz"` | `tar.gz` / `tgz` / `tar` / `zip` |
| `include` | []string | 见模板 | 要打包的相对路径（世界、配置、mods/plugins 等） |
| `exclude` | []string | 见模板 | 排除清单，支持 `*` 通配与目录前缀（`logs` 会连 `logs/latest.log` 一起排除） |
| `before_restore` | `*bool` | **缺省 true** | 回滚前先自动备份当前状态（失败则中止回滚，避免丢数据） |
| `stop_before_backup` | `*bool` | **缺省 false** | 备份前先 `save-all` / `save-off`，保证世界落盘一致 |

### 6.6 `[log]`

| 键 | 类型 | 模板默认 | 含义 |
| --- | --- | --- | --- |
| `rotate_size` | Size | `"20MB"`（= 20MiB） | 触发归档的大小阈值 |
| `keep_files` | int | `5` | 保留的历史日志份数 |
| `max_age_days` | int | `7` | 历史日志最长保留天数 |
| `compress` | `*bool` | **缺省 true** | 归档时压缩（gzip） |
| `rotate_restart` | `*bool` | **缺省 true** | 是否允许用优雅重启来切割 `latest.log`（**只在没有玩家在线、且距上次切割超过 `min_rotate_interval` 时才动手**） |
| `min_rotate_interval` | Duration | `"1h"` | 两次重启切割之间的最小间隔 |
| `manage_log4j2` | `*bool` | **缺省 false** | 托管服务端 `log4j2.xml`（写入前把原文件备份成 `log4j2.xml.bak`），让服务端自己按大小切割 |
| `console_lines` | int | `500` | 控制台环形缓冲行数（TUI/Web/`status -n` 取这里的最近行） |

### 6.7 `[scheduler]`

| 键 | 类型 | 模板默认 | 含义 |
| --- | --- | --- | --- |
| `enabled` | bool | `true` | 是否启用内置调度器 |
| `timezone` | string | `""` | 解释 cron 用的时区，留空表示本机时区（非法值会被拒绝） |
| `[[scheduler.jobs]]` | 数组 | 空 | 任务列表，见下表 |

| 任务字段 | 类型 | 含义 |
| --- | --- | --- |
| `name` | string | 任务名（必填、不能重复） |
| `schedule` | string | cron 表达式：5 段（分 时 日 月 周）或 6 段（含秒），也支持 `@hourly`、`@daily`、`@weekly`、`@monthly`、`@yearly`、`@every 1h30m` |
| `action` | string | `backup` / `restart` / `stop` / `start` / `command` |
| `instance` | string | 目标实例；留空表示所有实例或默认实例 |
| `command` | string | `action = "command"` 时要发送的指令（必填） |
| `enabled` | `*bool` | 缺省（不写）表示启用 |
| `label` | string | 自动备份文件名上的标签（默认 `auto`） |

调度器本身的运行状态记录在 `~/.nyatmc/state/scheduler.json`（含 pid 与各任务最近执行结果），
日志在 `state/scheduler.log`。后台运行用 `nyatmc schedule daemon --detach`。

### 6.8 `[web]`

| 键 | 类型 | 模板默认 | 含义 |
| --- | --- | --- | --- |
| `listen` | string | `"127.0.0.1:8080"` | 监听地址（必须形如 `host:port`，否则校验失败） |
| `token` | string | `""` | 访问令牌；留空且只监听回环时免鉴权 |
| `enable_write` | bool | `true` | 是否允许通过 Web 执行写操作（false 时写接口返回 403） |
| `refresh_seconds` | int | `3` | 前端自动刷新间隔 |
| `open` | bool | `false` | 启动后自动打开浏览器 |
| `max_log_lines` | int | `2000` | 单次日志接口返回的最大行数 |

### 6.9 `[tunnel]`

| 键 | 类型 | 模板默认 | 含义 |
| --- | --- | --- | --- |
| `provider` | string | `"cloudflared"` | 目前**仅支持** `cloudflared` |
| `bin` | string | `""` | cloudflared 可执行文件路径，留空自动查找或下载 |
| `protocol` | string | `"quic"` | cloudflared 传输协议（`quic` / `http2` / `auto`） |
| `token` | string | `""` | 命名隧道 token；留空使用快速隧道 |
| `hostname` | string | `""` | 命名隧道域名（用于展示） |
| `auto_install` | bool | `true` | 缺少 cloudflared 时自动下载 |
| `auto_start_with_web` | bool | `false` | `web serve` 时自动启动隧道，把仪表盘暴露到公网（退出时一并停止） |
| `extra_args` | []string | `[]` | 追加给 cloudflared 的参数 |

### 6.10 `[downloader]`

| 键 | 类型 | 模板默认 | 含义 |
| --- | --- | --- | --- |
| `mirror` | string | `""` | 镜像基地址，例如 `https://bmclapi2.bangbang93.com`（留空用官方源） |
| `timeout` | Duration | `"10m"` | 单次下载超时（必须 > 0） |
| `verify_checksum` | bool | `true` | 是否校验官方 SHA256/SHA1 |
| `retries` | int | `3` | 失败重试次数 |
| `cache_dir` | string | `""` | 下载缓存目录，留空用 `~/.nyatmc/cache` |
| `user_agent` | string | `nyatmc/<版本> (+https://github.com/yuzumikonami/nyatmc)` | 请求头（**不在模板里**，由 `ApplyDefaults` 补齐） |

### 6.11 `[mods]`

| 键 | 类型 | 模板默认 | 含义 |
| --- | --- | --- | --- |
| `provider` | string | `"modrinth"` | `modrinth`（免注册）/ `curseforge`（需要 API Key） |
| `curseforge_api_key` | string | `""` | CurseForge API Key；选了 curseforge 却没填会给出警告 |
| `mods_dir` | string | `"mods"` | 模组目录（相对服务器目录） |
| `plugins_dir` | string | `"plugins"` | 插件目录（相对服务器目录） |
| `game_version` | string | `""` | 留空表示沿用实例的服务端版本 |
| `loader` | string | `""` | 留空按实例类型推断（paper/fabric/vanilla…） |
| `limit` | int | `20` | 搜索返回条数 |

实例未显式设置版本时，`mod` 命令会沿用实例的 `server.type` / `server.minecraft_version` 过滤可用文件。

### 6.12 `[instances.<名字>]`（实例覆盖）

| 键 | 类型 | 含义 |
| --- | --- | --- |
| `path` | string | 服务器目录；留空用 `~/.nyatmc/instances/<名字>/server` |
| `enabled` | `*bool` | false 时调度器与 Web 会跳过该实例 |
| `type` / `minecraft_version` | string | 类型与版本快捷覆盖 |
| `description` | string | 备注 |
| `created_at` | string | 创建时间（RFC3339，`nyatmc create` 自动写） |
| `server` / `daemon` / `backup` / `log` | table | 细粒度覆盖，字段与全局同名段一致；**只覆盖你写了的键** |

示例：

```toml
[instances.survival]
type = "paper"
minecraft_version = "1.21.4"
description = "生存服"
  [instances.survival.server]
  port = 25566
  memory = "4G"
[instances.creative]
type = "fabric"
minecraft_version = "1.21.4"
```

实例名规则（`config.ValidateInstanceName`）：以字母/数字开头，之后可含字母、数字、下划线、短横线、点与中文，
最长 64 字符；`.` 与 `..` 被拒绝，也不允许路径分隔符（防目录穿越）。

### 6.13 校验与提醒

`nyatmc config validate`（以及任何命令启动时的隐式校验）会检查：`server.type` 白名单、
`server.memory` / `min_memory` 格式、`server.port` 范围（1–65535）、`jar_name` 不含路径分隔符、
`daemon.max_restarts ≥ 0`、`stop_timeout` / `startup_timeout` > 0、`backup.format` 白名单、`backup.keep ≥ 0`、
`log.keep_files ≥ 0`、`web.listen` 形如 `host:port`、`tunnel.provider` 仅 cloudflared、`mods.provider` 白名单、
`downloader.retries ≥ 0`、`downloader.timeout > 0`、`scheduler.timezone` 合法、每条任务的名字/cron/动作/实例/指令合法。

`Warnings()` 只提醒不阻断：CurseForge 缺 API Key、未同意 EULA、Web 监听非回环却没设令牌、
`log.rotate_restart = true`（会通过优雅重启切割日志）。

---

## 7. 多实例用法

每个实例有独立的服务器目录、端口、内存与备份目录，共用一个 supervisor 机制与一份配置。

```bash
# 生存服：25565，2G
nyatmc create survival --type paper --version 1.21.4 --port 25565 --memory 2G --eula

# 创造服：25566，4G，用 Fabric
nyatmc create creative --type fabric --version 1.21.4 --port 25566 --memory 4G --eula

nyatmc list                      # 看全部实例（* 是默认实例）
nyatmc use survival              # 之后省略实例名就操作 survival
nyatmc start survival --wait
nyatmc start creative --wait
nyatmc status --all
nyatmc backup --all
```

只覆盖某个实例的配置（不影响其它实例）：

```bash
nyatmc config set server.memory 6G survival
nyatmc config get server.port survival
```

接管已有服务器目录（不移动、不删除文件，只写 nyatmc 自己的元数据）：

```bash
nyatmc adopt myserver --path /srv/minecraft/paper
nyatmc start myserver
```

提醒：两个实例不能监听同一个端口（守护进程启动前会探测端口占用并提醒，服务端自己也会因
`Address already in use` 失败，日志解析会把它归为「端口被占用或无法绑定」）。

---

## 8. 日志轮转：为什么不能在线截断 `latest.log`

Minecraft 服务端（JVM）启动时就打开了 `logs/latest.log` 并**一直持有文件句柄**。因此：

* 用 `truncate` 清空文件，JVM 会继续在原偏移量上写，文件会变成一堆空洞（稀疏文件），日志内容错乱；
* 用 `mv` 改名也不行，JVM 仍写旧 inode，新文件不会被写入。

nyatmc 的对策是「**归档留存 + 择机优雅重启切割 + 可选托管 log4j2**」：

1. **启动前切割（最干净）**：每次拉起服务端前，如果 `logs/latest.log` 已经超过 `log.rotate_size`，
   就先归档到 `logs/nyatmc-archive/` 再清空——此刻 JVM 还没拿到句柄，是真正安全的切割
   （源码：`Supervisor.rotateLogsOnStart`）。
2. **运行中监控**：守护进程每 30 秒检查一次 `latest.log` 大小；一旦超限就先**复制归档一份**
   （不打断服务端），并按 `log.keep_files` / `log.max_age_days` 清理旧归档。
3. **无人在线时优雅重启完成切割**：当 `log.rotate_restart = true`（缺省 true）、
   当前**没有玩家在线**、且距上次切割超过 `min_rotate_interval`（默认 1h）时，nyatmc 才执行一次
   优雅重启（发 `stop` → 等 JVM 退出 → 再启动），于是走步骤 1 完成真正的切割。
   有人在线时只打印提醒并推迟，**不会打断玩家**。
4. **可选：让服务端自己切**：`log.manage_log4j2 = true` 时 nyatmc 会写入一份 `log4j2.xml`
   （`SizeBasedTriggeringPolicy` + `DefaultRolloverStrategy`，参数取自 `log.rotate_size` / `log.keep_files`），
   由 log4j2 负责滚动，原文件会被备份成 `log4j2.xml.bak`。这是纯增强，默认关闭。
5. **nyatmc 自己的日志**（`console.log`、`supervisor.log`）由 `pkg/logger` 的轮转写入器管理：
   按大小切割、可 gzip 压缩、按份数与天数清理——这些文件不归 JVM 管，可以随时切。

手动操作：`nyatmc logs --rotate`（归档一份，服务端已停止时顺带清空）、`nyatmc logs --truncate`、
`nyatmc logs --prune`（按配置清理）。

---

## 9. 常见问题（FAQ）

**Q1：启动后立刻失败 / 提示「服务端文件缺失」怎么办？**
`server.auto_download = true`（缺省开启）时 `nyatmc start` 会按 `server.type` /
`server.minecraft_version` 自动下载并校验服务端文件；也可以手动执行
`nyatmc server download [实例名]`。若仍失败，检查网络与镜像设置（`downloader.mirror`）、
磁盘空间，`nyatmc doctor` 会告诉你还缺什么（Java 版本、EULA、端口占用等）。

**Q2：`status` 说「未在运行」但进程还在？**
`nyatmc status` 优先通过控制通道问 supervisor，问不到就回退读 `state.json` 并标记
「状态来自落盘文件（守护进程未响应）」。这种情况通常意味着 supervisor 被外部杀死而服务端成了孤儿进程；
`nyatmc stop` 会清理这种失去守护的服务端进程。

**Q3：端口被占用 / `Address already in use`**
服务端启动前 nyatmc 会先探测 `server.port` 能否绑定并提醒；日志解析也会把该错误翻译成
「端口被占用或无法绑定，请检查 server.port 是否冲突」。处理方式：
改 `server.port`（`nyatmc config set server.port 25566 <实例>`）后 `nyatmc init --force` 重新写入
`server.properties`，或找出占用端口的程序（Linux：`ss -ltnp | grep 25565`；Windows：`netstat -ano | findstr 25565`）。

**Q4：EULA 没同意，服务端拒绝启动**
`server.eula` 缺省是 `false`，此时 `eula.txt` 写的是 `eula=false`，服务端会退出。
确认方式：`nyatmc init --eula`、`nyatmc create … --eula`，或
`nyatmc config set server.eula true`（等价于你已阅读并同意 <https://aka.ms/MinecraftEULA>）。
`nyatmc doctor` 会逐实例检查这一项。

**Q5：`OutOfMemoryError` / 服务器卡顿**
日志里出现 `OutOfMemoryError` 时 nyatmc 会把它标记为最近问题并提示调大 `server.memory`。
调大最大堆（`nyatmc config set server.memory 4G <实例>`）后需要重启实例生效；
同时确认机器物理内存够用（Termux 上尤其注意），必要时下调 `view_distance`、`max_players`。

**Q6：Java 版本要求**
`nyatmc doctor` 的检查项写明：**1.17+ 需要 Java 17，1.20.5+ 需要 Java 21**（更老的版本通常用 Java 8/11）。
Termux：`pkg install openjdk-17` / `openjdk-21`；Debian/Ubuntu：`apt install openjdk-21-jre-headless`。
nyatmc 只负责调用 `server.java_path`（默认 `java`），不管理 JDK。

**Q7：Termux 里进程被系统杀掉**
1. 启动前执行 `termux-wake-lock`（退出时 `termux-wake-unlock`）——`nyatmc doctor` 也会提示；
2. 在 Android 设置里关掉 Termux 的电池优化；
3. Android 12+ 的「phantom process killer」会杀后台子进程，必要时用 adb 关闭：
   `adb shell settings put global settings_enable_monitor_phantom_procs false`；
4. 好消息是 nyatmc 的 supervisor 是 `setsid` 派生的独立会话，退出 SSH/终端不会连带杀掉它。

**Q8：Windows 上怎么后台运行？**
直接 `nyatmc start` 即可：supervisor 用 `DETACHED_PROCESS | CREATE_NEW_PROCESS_GROUP | CREATE_NO_WINDOW`
派生，脱离当前控制台，关闭终端窗口不会停服。查看用 `nyatmc status`，停止用 `nyatmc stop`。
`nyatmc doctor` 在 Windows 上也会给出这条提示。

**Q9：用系统 cron 做定时备份 / 重启？**
`nyatmc backup`（以及 `nyatmc schedule`）就是为这个场景准备的：

```cron
# 每天 4 点备份默认实例，只保留 7 份，输出追加到日志
0 4 * * * /usr/local/bin/nyatmc backup --keep 7 >> /var/log/nyatmc-backup.log 2>&1
# 每周一 5 点重启并等待就绪
0 5 * * 1 /usr/local/bin/nyatmc restart --wait
```

也可以完全不用系统 cron，改用内置调度器（跨平台、有执行记录、能被 Web 展示）：

```bash
nyatmc schedule add daily-backup "0 4 * * *" backup --keep 7
nyatmc schedule daemon --detach
```

**Q10：`nyatmc status` 在脚本里怎么判断？**
用退出码：`0` 运行中、`3` 已停止、`4` 崩溃；需要结构化数据就加 `--json`：
`nyatmc status --json | jq .state`。

**Q11：改了配置要不要重启？**
大多数不用：supervisor 与 Web 都监听配置文件并热重载（`nyatmc config set` 也是立即生效）。
但**已经传给 JVM 的参数**（`server.memory`、`jvm_args`、端口等）需要重启实例才生效。

**Q12：`web` 打开是空白 / 访问被拒？**
确认 `web.listen` 与访问地址一致（默认只监听 `127.0.0.1`，手机/其它机器访问不到）；
若 `web.token` 非空，必须带 `?token=<令牌>`；若看到「只读模式」，检查 `web.enable_write`。

---

## 10. 从源码构建与发布

### 10.1 Makefile

| 目标 | 说明 |
| --- | --- |
| `make` / `make help` | 默认目标，打印全部目标与当前注入的版本信息 |
| `make build` | 编译本机平台 → `build/nyatmc`（Windows 是 `build/nyatmc.exe`） |
| `make build-all` | 交叉编译 linux/amd64、linux/arm64、windows/amd64 |
| `make linux-amd64` / `linux-arm64` / `windows-amd64` | 只编译单个平台 |
| `make termux` | 编译 linux/arm64 静态二进制并提示装到 `$PREFIX/bin` |
| `make test` / `vet` / `fmt` / `tidy` | `go test ./...` / `go vet ./...` / `go fmt ./...` / `go mod tidy` |
| `make run ARGS="status"` | 直接 `go run .` |
| `make install` | `go install`（装到 `GOBIN`） |
| `make checksums` | 为 `build/` 下产物生成 `sha256sums.txt` |
| `make release-snapshot` | `build-all` + `checksums`，本地发布预演 |
| `make clean` | 删除 `build/` |

版本信息通过 ldflags 注入 `cmd.Version` / `cmd.Commit` / `cmd.BuildDate`，
默认取 `git describe --tags --always --dirty`（失败则 `dev`）、`git rev-parse --short HEAD`、`git log -1 --format=%cI`，
可以用 `make build VERSION=1.2.3` 覆盖。交叉编译一律 `CGO_ENABLED=0`，并加 `-trimpath -ldflags "-s -w"`。

### 10.2 不依赖 make：`scripts/build.sh`

```bash
bash scripts/build.sh                 # 本机平台 → build/nyatmc
bash scripts/build.sh all             # 三个平台
bash scripts/build.sh linux/arm64     # 任意 GOOS/GOARCH（可给多个）
bash scripts/build.sh -h
```

脚本会检测 `go` 是否存在、`go.mod` 声明的 Go 版本是否满足，然后打印每个产物的**大小与 sha256**，
并写出 `build/sha256sums.txt`；构建 arm64 时会附上 Termux 安装提示。

### 10.3 发布流程（GitHub Actions）

`.github/workflows/release.yml`：

1. **test job**：`actions/checkout@v4` → `actions/setup-go@v5`（版本读 `go.mod`）→ `go vet ./...` → `go test ./...`；
2. **build job**：`fetch-depth: 0` 检出（`git describe` 需要标签）→ `make build-all VERSION=<tag>` → `sha256sum … > sha256sums.txt` → 上传 artifact；
3. **release job**：把 `nyatmc-linux-amd64`、`nyatmc-linux-arm64`、`nyatmc-windows-amd64.exe`、`sha256sums.txt`
   通过 `softprops/action-gh-release@v2` 上传到 Release（自动生成 release notes）。

触发方式：推送 `v*` 标签，或在 Actions 页面手动触发并填一个版本号。
发布前建议本地先 `make release-snapshot` 验证产物。

---

## 11. 项目结构与架构

### 11.1 运行模型

```text
nyatmc start                    nyatmc status / stop / restart
      │ 派生（脱离终端）                     │
      ▼                                     │ 读 endpoint.json
 ┌──────────────────────────┐               │ 连回环 TCP + 令牌
 │ supervisor（每实例一个）  │◀──────────────┘
 │  · 持有实例锁与状态机     │
 │  · 启动/守护 java 进程    │
 │  · 看门狗（窗口内计数）   │
 │  · 日志轮转与归档         │
 │  · 控制通道服务端         │
 └──────────┬───────────────┘
            │ stdin/stdout
            ▼
      java -Xms… -Xmx… -jar server.jar --nogui
```

* **一个实例一个 supervisor**：`nyatmc start` 派生 `nyatmc __supervise --config … --instance …`，
  `cmd.Dir` 设为实例根目录；Unix 用 `setsid`、Windows 用 `DETACHED_PROCESS` 脱离终端。
* **本地控制通道**（`pkg/socket`）：只监听 **回环地址的 TCP**（`127.0.0.1:0` 随机端口），
  端口 + 32 字节随机令牌 + pid 写进实例目录的 `endpoint.json`（权限 0600）。
  报文是逐行 JSON，天然支持「订阅日志」这类流式场景；协议版本不匹配时客户端会提示重启 supervisor。
  选择 TCP 而不是 Unix socket / 命名管道，是为了在 Termux、Linux、Windows 上行为一致且零依赖。
* **状态机**：`stopped` / `starting` / `running` / `stopping` / `restarting` / `crashed` / `unknown`，
  每次变化都原子写 `state.json` 并推送给订阅者（TUI/Web）。
* **就绪判定**：`internal/logwatch` 增量解析服务端输出——`Done (…)!` 视为就绪，
  `joined/left the game`、`logged in with entity id`、`lost connection`、`Disconnecting` 维护玩家列表，
  还解析 TPS、`Can't keep up!` 的落后毫秒数、版本号、WARN/ERROR 计数，并把 EULA、端口绑定失败、
  `OutOfMemoryError` 翻译成中文提示。**不需要服务端装任何插件**。
* **优雅关闭**：向 stdin 写 `stop_command`（默认 `stop`）→ 等 `stop_timeout` → 仍不退就杀掉整个进程组
  （Unix `kill(-pgid)`、Windows `taskkill /F /T`）。`Ctrl+C` 第一次触发优雅关闭，第二次强制退出。
* **热重载**：`internal/config.Manager` 监听配置文件所在**目录**（兼容编辑器「写临时文件 + 改名」），
  250ms 去抖，自己写盘后的 700ms 内的变更会跳过，避免自触发。
* **三个前端共享同一个 supervisor**：CLI 直接走控制通道，TUI 订阅事件流（拿不到推送时退化为每秒轮询），
  Web 提供 REST + `go:embed` 前端，因此状态不会分叉。

### 11.2 依赖

`go.mod` 直接依赖 `github.com/spf13/cobra`（CLI）；配置用 `github.com/pelletier/go-toml/v2`，
热重载用 `github.com/fsnotify/fsnotify`，终端检测用 `golang.org/x/term`，Web 用 `github.com/gin-gonic/gin`，
调度用 `github.com/robfig/cron/v3`。以上都是纯 Go 实现，交叉编译不需要 cgo。

---

## 12. 许可证与致谢

本项目以 **GNU General Public License v3.0 or later**（GPL-3.0-or-later）发布，
完整条款见仓库根目录的 [LICENSE](LICENSE)。

致谢：

* **AetherCraft** —— nyatmc 的前身 Shell 脚本，本项目是它的 Go 重写与进化；
* **PaperMC / Fabric / Purpur / SpigotMC / Mojang** —— 服务端与元数据接口；
* **Modrinth / CurseForge** —— 模组与插件来源；
* **Cloudflare** —— `cloudflared` 内网穿透；
* 开源依赖：`spf13/cobra`、`pelletier/go-toml`、`fsnotify`、`gin-gonic/gin`、`robfig/cron`、`golang.org/x/term`。

---

**文档反馈**：如果发现本文与 `nyatmc --help` 或源码不一致，请以源码为准并提 issue 指出。
