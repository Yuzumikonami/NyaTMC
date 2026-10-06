<div align="center">

# NyaTMC

**用 Go 写的 Minecraft 服务器全能管理工具**

TUI 主界面 · CLI 脚本接口 · Web 远程补充 · 单二进制 · 零依赖 · 跨平台

[![Status](https://img.shields.io/badge/status-开发中-orange)](#-开发进度)
[![Go](https://img.shields.io/badge/Go-1.27-00ADD8?logo=go)](https://go.dev)
[![License](https://img.shields.io/badge/License-GPLv3-blue)](LICENSE)

</div>

---

> ⚠️ **项目正在开发中**
> 当前仓库处于早期阶段，接口和目录结构随时可能变动，**尚不可用于生产环境**。
> 请勿在正式服上部署，直到第一个正式 Release 发布。

---

## 这是什么

`NyaTMC` 的目标是让 Minecraft 服务器的开服、管服、备份、穿透全部收进**一个可执行文件**里。

设计核心只有一句话：**TUI 是一等公民，`internal/app` 是唯一真相源，CLI 和 Web 都只是它的壳。**

```
main.go
  ↓
cmd/ (Cobra 壳)    tui/ (Bubble Tea 壳)    web/ (Gin 壳)
  ↓                  ↓                       ↓
             internal/app/  ← 唯一业务入口
                    ↓
   daemon / config / backup / scheduler / downloader
                    ↓
             pkg/ (logger、procutil)
```

三层 UI 互不导入，业务逻辑全部沉淀在 `internal/app`。TUI 本质就是调用 `app` 并把结果可视化。

## 特性规划

| 模块 | 能力 | 状态 |
|---|---|---|
| 实例管理 | 创建 / 启动 / 停止 / 重启 / 状态查询 | 🚧 开发中 |
| 进程守护 | 跨平台子进程管理、优雅关闭、看门狗自动重启 | 🚧 开发中 |
| 实时 TUI | 日志流、命令输入、信息面板、多实例切换 | 🚧 开发中 |
| 备份恢复 | tar.gz 打包、自动轮转、带确认的一键恢复 | 📋 计划 |
| 定时调度 | cron 表达式定时备份、定时重启 | 📋 计划 |
| 服务端下载 | Paper / Fabric / Spigot 自动拉取 + SHA256 校验 | 📋 计划 |
| 内网穿透 | 一键 Cloudflare Tunnel，状态栏显示公网地址 | 📋 计划 |
| Web 面板 | 浏览器实时日志 + 远程控制 | 📋 计划 |
| 自动更新 | 基于 GitHub Release 自更新 | 📋 计划 |

## 跨平台支持

| 平台 | 状态 |
|---|---|
| Linux (amd64/arm64) | ✅ 目标平台 |
| Windows | ✅ 目标平台 |
| Android / Termux | ✅ 目标平台（不依赖 systemd，纯子进程） |

跨平台细节已在设计阶段处理：Unix 用 `Setpgid` 杀进程组，Windows 用 `taskkill /T /F` 或 Job Object，路径统一走 `os.UserHomeDir()`。

## 快速开始

> 目前还没有 Release 产物，想尝鲜请自行从源码构建。

```bash
git clone https://github.com/Yuzumikonami/NyaTMC.git
cd NyaTMC
go build -o nyatmc .

./nyatmc version
```

### 安装

预编译二进制、`go install` 支持情况将在首次 Release 时给出说明。

> 注：`go install` 方式**不支持自动更新**，如果用这种方式安装，更新时请走原安装方式重新拉取。

## 实例目录结构

所有实例数据统一收在 `~/.nyatmc/` 下：

```
~/.nyatmc/
├── config.toml                    # 全局配置
├── instances/
│   └── <name>/
│       ├── instance.toml          # 实例配置
│       ├── server.jar
│       ├── eula.txt
│       ├── server.properties
│       ├── world/  world_nether/  world_the_end/
│       ├── plugins/  mods/  config/
│       ├── logs/
│       ├── backups/
│       └── nyatmc.pid
└── logs/
    └── nyatmc.log                 # 工具自身日志
```

## 技术选型

| 用途 | 选型 |
|---|---|
| CLI | `spf13/cobra` |
| TUI | `charmbracelet/bubbletea` + `bubbles` + `lipgloss` |
| 配置 | `pelletier/go-toml/v2` |
| 日志 | 标准库 `log/slog` |
| 调度 | `robfig/cron/v3` |
| Web | `gin-gonic/gin` + `go:embed` |
| 前端 | 原生 HTML + 少量 JS |

## 开发进度

| 阶段 | 内容 | 状态 |
|---|---|---|
| 0 | 地基：go.mod、版本号、配置加载 | ✅ 完成 |
| 1 | daemon 核心：进程启停、日志管道 | 🚧 进行中 |
| 2 | app 服务层：Manager / Instance / 事件总线 | 🚧 进行中 |
| 3 | TUI MVP：日志 + 命令 + 启停 | 📋 计划 |
| 4 | TUI 完整版：信息面板 / 多实例 / 全快捷键 | 📋 计划 |
| 5 | 备份 / 恢复 | 📋 计划 |
| 6 | CLI 命令全量对齐 | 📋 计划 |
| 7 | 调度器 + 下载器 | 📋 计划 |
| 8 | Cloudflare Tunnel | 📋 计划 |
| 9 | Web 仪表盘 | 📋 计划 |

详细路线图见 [`goal.md`](goal.md)。

## 贡献

项目还在打地基，暂时不太适合直接提 PR。如果你有想法，欢迎先开 Issue 聊聊喵！。

## 许可证

[GPL-3.0](LICENSE)

---

<div align="center">

<a href="https://www.konatonami.top/">© Yuzumikonami(柚见小南)</a> 用 🐾 和 ❤️ 搭建喵w ~ 🐾
<br>注：本Readme使用Deepseek v4.1撰写

</div>
