package socket

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestEndpointRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "endpoint.json")
	ep := Endpoint{
		Version:   ProtocolVersion,
		Instance:  "survival",
		Network:   "tcp",
		Address:   "127.0.0.1:12345",
		Token:     "abc",
		PID:       42,
		StartedAt: time.Now().Truncate(time.Second),
	}
	if err := WriteEndpoint(path, ep); err != nil {
		t.Fatalf("写入端点失败: %v", err)
	}
	got, err := ReadEndpoint(path)
	if err != nil {
		t.Fatalf("读取端点失败: %v", err)
	}
	if got.Address != ep.Address || got.Token != ep.Token || got.Instance != ep.Instance {
		t.Errorf("端点内容不一致: %+v", got)
	}
	// 端点文件里含令牌，在 Unix 上必须是 0600（Windows 不按 Unix 权限位解释，跳过）
	if runtime.GOOS != "windows" {
		st, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if perm := st.Mode().Perm(); perm != 0o600 {
			t.Errorf("端点文件权限 = %o，期望 600", perm)
		}
	}

	if err := RemoveEndpoint(path); err != nil {
		t.Fatalf("删除端点失败: %v", err)
	}
	if _, err := ReadEndpoint(path); !errors.Is(err, ErrNotRunning) {
		t.Errorf("端点不存在时应返回 ErrNotRunning，得到 %v", err)
	}
}

// startTestServer 起一个测试用控制通道。
func startTestServer(t *testing.T, token string) (*Server, Endpoint) {
	t.Helper()
	srv, err := Listen(Endpoint{Instance: "test", Token: token}, func(ctx context.Context, req Request, send func(Response) error) error {
		switch req.Cmd {
		case "ping":
			return send(Response{OK: true})
		case "echo":
			return send(Response{OK: true, Data: req.Args})
		case "fail":
			return errors.New("这是故意的失败")
		case "stream":
			for i := 0; i < 3; i++ {
				data, _ := json.Marshal(map[string]int{"i": i})
				if err := send(Response{OK: true, Stream: true, Data: data}); err != nil {
					return err
				}
			}
			return nil
		default:
			return errors.New("未知命令")
		}
	})
	if err != nil {
		t.Fatalf("启动控制通道失败: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return srv, srv.Endpoint()
}

func TestCallAndAuth(t *testing.T) {
	_, ep := startTestServer(t, "secret-token")

	if err := Ping(ep, 2*time.Second); err != nil {
		t.Fatalf("Ping 失败: %v", err)
	}

	// 正确令牌
	c, err := Dial(ep, 2*time.Second)
	if err != nil {
		t.Fatalf("连接失败: %v", err)
	}
	defer c.Close()
	var out map[string]string
	if err := c.Call(context.Background(), "echo", map[string]string{"hello": "世界"}, &out); err != nil {
		t.Fatalf("Call 失败: %v", err)
	}
	if out["hello"] != "世界" {
		t.Errorf("回显 = %v", out)
	}

	// 错误令牌必须被拒绝
	bad := ep
	bad.Token = "wrong"
	c2, err := Dial(bad, 2*time.Second)
	if err != nil {
		t.Fatalf("连接失败: %v", err)
	}
	defer c2.Close()
	if err := c2.Call(context.Background(), "ping", nil, nil); err == nil {
		t.Fatal("无效令牌应被拒绝")
	}
}

func TestHandlerError(t *testing.T) {
	_, ep := startTestServer(t, "t")
	c, err := Dial(ep, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	err = c.Call(context.Background(), "fail", nil, nil)
	if err == nil {
		t.Fatal("应返回错误")
	}
}

func TestStream(t *testing.T) {
	_, ep := startTestServer(t, "t")
	c, err := Dial(ep, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	var got []int
	err = c.Stream(context.Background(), "stream", nil, func(resp Response) error {
		var v map[string]int
		if err := json.Unmarshal(resp.Data, &v); err != nil {
			return err
		}
		got = append(got, v["i"])
		return nil
	})
	if err != nil {
		t.Fatalf("流式读取失败: %v", err)
	}
	if len(got) != 3 || got[0] != 0 || got[2] != 2 {
		t.Errorf("流式数据 = %v，期望 [0 1 2]", got)
	}
}

func TestDialRejectsProtocolMismatch(t *testing.T) {
	_, ep := startTestServer(t, "t")
	wrong := ep
	wrong.Version = ProtocolVersion + 1
	if _, err := Dial(wrong, time.Second); err == nil {
		t.Fatal("协议版本不一致时应拒绝连接")
	}
}

func TestCallPathWithoutEndpoint(t *testing.T) {
	dir := t.TempDir()
	if err := CallPath(context.Background(), filepath.Join(dir, "missing.json"), "ping", nil, nil); !errors.Is(err, ErrNotRunning) {
		t.Errorf("端点不存在时应返回 ErrNotRunning，得到 %v", err)
	}
	if PingPath(filepath.Join(dir, "missing.json"), time.Second) {
		t.Error("端点不存在时 PingPath 应为 false")
	}
}

func TestListenUsesRandomPortAndLoopback(t *testing.T) {
	srv, _ := startTestServer(t, "")
	ep := srv.Endpoint()
	if ep.Address == "" {
		t.Fatal("应返回真实监听地址")
	}
	if ep.Token == "" {
		t.Error("未提供令牌时应自动生成")
	}
	if ep.Version != ProtocolVersion {
		t.Errorf("协议版本 = %d", ep.Version)
	}
}
