# `nyatmc` 修订版完整目标喵 🐱

**核心转向**：TUI 是一等公民，`internal/app` 是唯一真相源，CLI/Web 都是它的壳。

---

## 一、项目定位（一句话）

> **`nyatmc`** 是用 Go 编写的 Minecraft 服务器全能管理工具，以 **TUI 为主界面**，CLI 为脚本接口，Web 为远程补充。单二进制、零依赖、跨平台（Termux/Linux/Windows）。

---

## 二、架构铁律

### 1. 分层依赖方向（单向，禁止反向）

```
main.go
  ↓
cmd/  (Cobra 壳)   tui/  (Bubble Tea 壳)   web/  (Gin 壳)
  ↓                    ↓                       ↓
        internal/app/  (唯一业务入口)
              ↓
   daemon / config / backup / scheduler / downloader
              ↓
        pkg/  (通用工具：logger、socket)
```

**规则**：
- `cmd/`、`tui/`、`web/` **互不导入**
- 三者都只调 `internal/app`
- `internal/app` 不导入任何 UI 库
- UI 层可以导入 `internal/app` 的实体类型

### 2. 状态唯一来源

所有实例状态由 **`app.Manager`** 持有。TUI 和 CLI 操作的是**同一个 Manager 实例**（进程内）。CLI 单次运行、TUI 常驻，各自创建 Manager，但操作的是磁盘上同一份实例数据。

### 3. 实例目录契约（所有功能的地基）

```
~/.nyatmc/
├── config.toml                    # 全局配置
├── instances/
│   └── <name>/                    # 实例根目录
│       ├── instance.toml          # 实例配置
│       ├── server.jar
│       ├── eula.txt
│       ├── server.properties
│       ├── world/  world_nether/  world_the_end/
│       ├── plugins/  mods/  config/
│       ├── logs/
│       │   ├── latest.log
│       │   └── archive/
│       ├── backups/
│       └── nyatmc.pid             # 运行时 PID
└── logs/
    └── nyatmc.log                 # 工具自身日志
```

**这个契约一旦定下，所有模块只需知道自己那一段。**

---

## 三、目录结构（修订版）

```
nyatmc/
├── go.mod
├── main.go
├── README.md
│
├── cmd/                           # CLI 壳（阶段 6 才做）
│   ├── root.go
│   ├── start.go  stop.go  restart.go  status.go
│   ├── backup.go  restore.go
│   ├── init.go
│   ├── tui.go                     # nyatmc tui（转调 tui 包）
│   ├── config_cmd.go
│   ├── mod.go
│   └── web.go
│
├── internal/
│   ├── app/                       # ★ 核心业务层（唯一真相源）
│   │   ├── manager.go             # Manager：持有所有实例
│   │   ├── instance.go            # Instance：单个实例的聚合
│   │   ├── lifecycle.go           # Start/Stop/Restart
│   │   ├── stats.go               # TPS/内存/玩家采集
│   │   └── events.go              # 事件总线（TUI/Web 订阅）
│   │
│   ├── daemon/                    # 进程守护（app 依赖它）
│   │   ├── process.go             # 通用 Process 抽象
│   │   ├── process_unix.go        # build tag: !windows
│   │   ├── process_windows.go     # build tag: windows
│   │   ├── pipe.go                # stdin/stdout 管道
│   │   └── watch.go               # 看门狗：崩溃重启
│   │
│   ├── config/
│   │   ├── global.go              # ~/.nyatmc/config.toml
│   │   ├── instance.go            # 实例级 instance.toml
│   │   └── defaults.go
│   │
│   ├── backup/
│   │   ├── create.go
│   │   ├── restore.go
│   │   └── rotate.go
│   │
│   ├── downloader/
│   │   ├── paper.go  fabric.go  spigot.go
│   │   └── checksum.go
│   │
│   ├── scheduler/
│   │   └── cron.go
│   │
│   ├── tunnel/
│   │   └── cloudflared.go
│   │
│   ├── tui/                       # ★ TUI 壳（阶段 3-4）
│   │   ├── app.go                 # tea.Program 启动入口
│   │   ├── model.go               # 根 Model
│   │   ├── update.go              # 消息路由
│   │   ├── view.go                # 布局组装
│   │   ├── keys.go                # 键位表
│   │   ├── ring.go                # 环形日志缓冲
│   │   └── components/
│   │       ├── logview/           # 日志视口
│   │       ├── infopanel/         # TPS/玩家/内存
│   │       ├── cmdinput/          # 命令输入
│   │       ├── instlist/          # 实例列表
│   │       └── statusbar/         # 状态栏
│   │
│   └── web/                       # Web 壳（阶段 9，可选）
│       ├── server.go
│       └── static/                # go:embed
│
├── pkg/
│   ├── logger/
│   │   └── rotate.go              # 日志轮转
│   └── procutil/                  # 跨平台进程工具
│
├── embed/
│   └── default_config.toml
│
└── scripts/
    ├── build.sh
    └── install.sh
```

---

## 四、分阶段执行路线图（严格按序）

> **每个阶段结束必须 `go build` 通过 + 有可验证的交付物**。前一阶段不通过，不进下一阶段。

### 🎯 阶段 0：地基（预计 0.5 天）

**目标**：能编译、能读配置。

**交付物**：
- `go.mod` + 目录骨架
- `main.go` 打印 `nyatmc version`
- `internal/config` 能加载 `~/.nyatmc/config.toml`
- `embed/default_config.toml` 内置默认配置

**验收**：
```bash
go build ./...                    # 通过
go run . version                  # 输出版本
go run . init                     # 创建 ~/.nyatmc/ 结构
cat ~/.nyatmc/config.toml         # 有内容
```

**不做**：任何 UI、任何业务。

---

### 🎯 阶段 1：daemon 核心（预计 3 天）★ 最难

**目标**：能启动/停止一个 MC 服务器，能收发日志和命令。

**交付物**：
- `daemon.Process`：启动、停止、发命令、广播日志
- 跨平台：`process_unix.go` + `process_windows.go`（build tag）
- PID 文件读写
- 优雅关闭：stdin 发 `stop` → 等待 → 超时强杀

**验收**（写个临时 main 或测试）：
```go
p := daemon.New("/path/to/instance", "-Xmx2G", "server.jar", log)
p.Start(ctx)
// 订阅日志
ch := p.Subscribe()
// 从终端读一行，发过去
p.SendCommand("say hello")
// Ctrl+C 触发 Stop，服务器收到 stop 优雅关闭
```

**关键决策点**（写下来，别改）：
- 进程组处理：Unix 用 `Setpgid`，Windows 用 Job Object 或 `taskkill /T`
- 日志行 buffer：**至少 1MB**（MC 单行可能超 64KB）
- 广播用**非阻塞 send**，慢订阅者丢消息不拖累守护

**不做**：多实例、看门狗、UI。

---

### 🎯 阶段 2：app 服务层（预计 1.5 天）

**目标**：把 daemon 包成业务 API，供 UI 层调用。

**交付物**：
- `app.Manager`：加载所有实例、按名查找
- `app.Instance`：聚合 config + process + 状态
- `app.Manager.Start(name)` / `.Stop(name)` / `.Send(name, cmd)`
- 事件总线：`manager.Subscribe() <-chan Event`（日志、状态变化）

**验收**：
```go
mgr, _ := app.NewManager()
inst, _ := mgr.Get("survival")
mgr.Start(ctx, "survival")
for ev := range mgr.Subscribe() {
    fmt.Println(ev)   // 能看到日志事件
}
```

**不做**：UI、备份、调度。

---

### 🎯 阶段 3：TUI MVP（预计 2.5 天）★ 主力交互

**目标**：`nyatmc tui` 能看日志、发命令、启停实例。

**界面**（最小版）：
```
┌─ nyatmc ── survival ── ● running ────────────────┐
│                                                   │
│  [12:00:01] Starting server                       │
│  [12:00:03] Done (2.1s)!                          │
│  [12:00:05] Steve joined the game                 │
│  ...                                              │
│                                                   │
├───────────────────────────────────────────────────┤
│ > say hello_                                      │
├───────────────────────────────────────────────────┤
│ s:start  S:stop  r:restart  b:backup  q:quit      │
└───────────────────────────────────────────────────┘
```

**交付物**：
- `tui/app.go`：`Run(manager)` 入口
- `tui/model.go`：根 Model
- `tui/update.go`：按键 + 流式日志消息路由
- `tui/view.go`：布局
- `tui/ring.go`：环形缓冲（2000 行）
- `logview` + `cmdinput` + `statusbar` 三个组件

**关键模式**（必须掌握）：
```go
// 流式日志消费：每次消费后重新挂 Cmd
func waitForLog(ch <-chan app.Event) tea.Cmd {
    return func() tea.Msg {
        ev, ok := <-ch
        if !ok { return nil }
        return logMsg(ev)
    }
}
```

**验收**：
```bash
nyatmc tui
# 界面正常渲染
# 输入 say hi 回车，日志区出现服务器响应
# 按 s 启动，按 S 停止
# q 优雅退出
```

**不做**：信息面板、多实例、备份快捷键。

---

### 🎯 阶段 4：TUI 完整版（预计 2.5 天）

**目标**：加入信息面板、多实例切换、全部快捷键。

**界面**（完整版）：
```
┌─ nyatmc ── [survival] ── ● running ──────────────┐
│                                                   │
│  ┌── 日志 ─────────────────┐ ┌── 信息 ─────────┐ │
│  │ [12:00:01] Starting...  │ │ TPS:  20.0      │ │
│  │ [12:00:03] Done!        │ │ 玩家: 3/20      │ │
│  │ ...                     │ │ 内存: 1.2/2.0G  │ │
│  │                         │ │ 运行: 2h13m     │ │
│  │                         │ │ 端口: 25565     │ │
│  └─────────────────────────┘ └─────────────────┘ │
│                                                   │
│ > _                                               │
├───────────────────────────────────────────────────┤
│ Tab:切换  b:备份  e:编辑配置  L:日志轮转  q:退出 │
└───────────────────────────────────────────────────┘
```

**交付物**：
- `infopanel` 组件 + `app.Stats()` 定时轮询（`tea.Tick` 每 2s）
- `instlist` 组件 + 多实例切换（`Tab` 或数字键）
- `keys.go` 集中键位定义
- 备份快捷键 `b` → 调 `app.Backup()`
- 配置编辑模式 `e`

**验收**：
- 顶部能切实例
- 信息面板数字会跳
- `b` 按键后 `backups/` 出现文件

---

### 🎯 阶段 5：备份/恢复（预计 1 天）

**目标**：TUI 里能备份、恢复。

**交付物**：
- `internal/backup`：tar.gz 打包 + 轮转 + 恢复
- 备份前 `save-all` → `save-off` → 备份 → `save-on`
- TUI 里 `b` 备份、`R` 恢复（带确认弹窗）

**验收**：
- 备份文件生成在 `instances/<name>/backups/`
- 超过 `backup_keep` 自动删最旧
- 恢复后世界能启动

---

### 🎯 阶段 6：CLI 命令（预计 1 天）

**目标**：给 `internal/app` 套 Cobra 壳，所有 TUI 功能有 CLI 对应。

**交付物**：
- `cmd/` 全部命令
- `nyatmc tui` 转调 `tui.Run()`
- 全局 `--config` / `--instance` flag

**验收**：
```bash
nyatmc start survival
nyatmc status survival
nyatmc backup survival
nyatmc stop survival
nyatmc tui
```

**关键**：CLI 每个命令的 `RunE` 里**只做**：解析 flag → 调 `app.Manager` → 打印结果。**不写业务逻辑**。

---

### 🎯 阶段 7：调度器 + 下载器（预计 1.5 天）

**目标**：定时任务 + 自动下载 jar。

**交付物**：
- `scheduler`：`robfig/cron/v3`，支持 `schedule backup "0 4 * * *"`
- `downloader`：Paper API 拉最新版 + SHA256 校验
- TUI 里显示调度任务列表

**验收**：
- 配置定时备份，到点自动执行
- `nyatmc create new --type paper` 自动下载 jar

---

### 🎯 阶段 8：Tunnel（预计 1 天）

**目标**：一键 Cloudflare 穿透。

**交付物**：
- `tunnel.Manager`：启动 `cloudflared` 子进程，解析 stderr 拿公网 URL
- TUI 里快捷键 `t` 开关穿透，状态栏显示 URL

**验收**：
- 按 `t` 后状态栏出现 `https://xxx.trycloudflare.com`

---

### 🎯 阶段 9：Web 仪表盘（预计 3 天，可选）

**目标**：远程查看和控制。

**交付物**：
- `internal/web`：Gin + `go:embed` 静态文件
- SSE 推送日志
- 复用 `app.Manager` 和事件总线

**验收**：
- 浏览器能看到实时日志
- 能启停、备份

---

## 五、时间总览

| 阶段 | 内容 | 天数 | 累计 |
|---|---|---|---|
| 0 | 地基 | 0.5 | 0.5 |
| 1 | daemon 核心 | 3 | 3.5 |
| 2 | app 服务层 | 1.5 | 5 |
| 3 | TUI MVP | 2.5 | 7.5 |
| 4 | TUI 完整版 | 2.5 | 10 |
| 5 | 备份/恢复 | 1 | 11 |
| 6 | CLI | 1 | 12 |
| 7 | 调度 + 下载 | 1.5 | 13.5 |
| 8 | Tunnel | 1 | 14.5 |
| 9 | Web（可选） | 3 | 17.5 |

**~15 天可出一个功能完整的 TUI 版本**（阶段 0-8），Web 视需求追加。

---

## 六、技术选型（已锁定，不改）

| 用途 | 库 | 理由 |
|---|---|---|
| CLI 框架 | `spf13/cobra` | 生态标准 |
| TUI 框架 | `charmbracelet/bubbletea` | Elm 架构，可测试 |
| TUI 组件 | `charmbracelet/bubbles` | 官方组件 |
| TUI 样式 | `charmbracelet/lipgloss` | 声明式样式 |
| 配置 | `pelletier/go-toml/v2` | **不用 viper**（TOML 处理乱） |
| 日志 | 标准库 `log/slog` | 零依赖，结构化 |
| 调度 | `robfig/cron/v3` | 成熟稳定 |
| Web | `gin-gonic/gin` | 快，够用 |
| 前端 | 原生 HTML + 少量 JS | 拒绝框架 |

---

## 七、跨平台硬约束（写死在设计里）

| 问题 | 方案 |
|---|---|
| Unix 信号 | `syscall.SIGTERM` + `Setpgid` 杀进程组 |
| Windows 无信号 | `taskkill /T /F` 或 Job Object |
| Termux 无 systemd | 不依赖系统服务，纯子进程 |
| `~` 路径 | `os.UserHomeDir()`，不用硬编码 |
| java 路径 | 配置里允许绝对路径，默认查 `PATH` |
| 中文日志 | 强制 UTF-8，Windows 需要 `chcp 65001` 提示 |

---

## 八、反模式清单（贴墙上看）🚫

1. **业务逻辑写在 `cmd/` 或 `tui/` 里** → 必须抽到 `app`
2. **`os.Exit()` 在业务层** → 返回 error
3. **用 `Process.Kill()` 关 MC** → 必须 stdin 发 `stop`
4. **TUI 里开 goroutine 直接改 Model** → 必须走 `tea.Msg`
5. **日志写 stdout** → TUI 会花屏，必须 stderr
6. **`bufio.Scanner` 用默认 buffer** → MC 日志会断
7. **viper 管 TOML** → 多实例会乱
8. **UI 层直接 import `daemon`** → 必须经 `app`
9. **一次性实现所有功能** → 按阶段，每阶段可运行

---

## 九、第一个 commit 应该是什么

```
commit 1: chore: 初始化项目骨架
  - go.mod
  - main.go（打印版本）
  - internal/config（加载 TOML）
  - embed/default_config.toml
  - 目录结构
```

跑通 `go run . version` 和 `go run . init`，就算完成阶段 0。

---

**下一步**：要不要我把**阶段 0 + 阶段 1**的完整可运行代码写出来？阶段 1 的 `daemon.Process` 是整块最难的部分，跨平台细节容易踩坑，我可以给你两个平台都写好的版本喵～

尽量实现自动更新 支持Github release自动更新 go install不支持 提示自行用原安装方法更新

tui本质为调用app并解析结果可视化