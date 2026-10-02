// Command fakeserver 是一个“假 Minecraft 服务端”，用于 nyatmc 的集成测试。
//
// 它模仿真实服务端的控制台行为：打印启动日志与 Done 就绪行、接受 stop / list / tps /
// say 指令，并把玩家上下线写进日志。这样在没有 Java、没有网络的环境里也能验证
// supervisor、控制通道、日志解析、看门狗、备份等完整链路。
//
// 它位于 testdata 目录下，因此不会被 `go build ./...` 编译进项目。
//
// 用法（由 scripts/smoke-test.ps1 调用）：
//
//	go build -o fakeserver.exe ./internal/daemon/testdata/fakeserver
//
// 环境变量：
//
//	FAKE_CRASH=1        启动后立刻以退出码 1 结束（用于验证看门狗）
//	FAKE_START_DELAY=3  延迟 N 秒才输出 Done（用于验证 --wait 与启动超时）
//	FAKE_NO_READY=1     永不输出 Done（用于验证启动超时提示）
package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

func logf(format string, args ...any) {
	fmt.Printf("[%s] [Server thread/INFO]: %s\n", time.Now().Format("15:04:05"), fmt.Sprintf(format, args...))
}

func main() {
	fmt.Printf("[%s] [main/INFO]: Starting minecraft server version 1.21.4\n", time.Now().Format("15:04:05"))
	fmt.Printf("[%s] [main/INFO]: Loading properties\n", time.Now().Format("15:04:05"))

	if os.Getenv("FAKE_CRASH") == "1" {
		fmt.Printf("[%s] [main/ERROR]: 模拟崩溃：无法分配内存\n", time.Now().Format("15:04:05"))
		os.Exit(1)
	}

	delay := 0
	if v := os.Getenv("FAKE_START_DELAY"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			delay = n
		}
	}
	if delay > 0 {
		time.Sleep(time.Duration(delay) * time.Second)
	}

	if os.Getenv("FAKE_NO_READY") != "1" {
		logf("Done (%d.%03ds)! For help, type \"help\"", 1+delay, 234)
	}
	logf("Steve joined the game")
	logf("TPS from last 1m, 5m, 15m: 19.95, 20.0, 20.0")

	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case line == "":
			continue
		case line == "stop":
			logf("Stopping the server")
			logf("Steve left the game")
			fmt.Printf("[%s] [Server thread/INFO]: Saving worlds\n", time.Now().Format("15:04:05"))
			time.Sleep(300 * time.Millisecond)
			os.Exit(0)
		case line == "list":
			logf("There are 1 of a max of 20 players online: Steve")
		case line == "tps":
			logf("TPS from last 1m, 5m, 15m: 19.98, 20.0, 20.0")
		case strings.HasPrefix(line, "save-all"):
			logf("Saved the game")
		case strings.HasPrefix(line, "say "):
			logf("[Server] %s", strings.TrimPrefix(line, "say "))
		default:
			logf("Unknown or incomplete command: %s", line)
		}
	}
	if err := sc.Err(); err != nil {
		fmt.Printf("[%s] [main/WARN]: 读取标准输入失败：%v\n", time.Now().Format("15:04:05"), err)
	}
}
