// Package socket 实现了 nyatmc 进程间的本地控制通道（IPC）。
//
// 设计要点：
//   - 只监听回环地址的 TCP 端口，不依赖 Unix socket / 命名管道，因此在
//     Termux、Linux、Windows 上行为一致，也不需要任何第三方依赖；
//   - 端口、随机令牌、pid 写入实例目录下的 endpoint.json（权限 0600），
//     客户端读取该文件后连接，令牌用于防止本机其它用户操纵服务器；
//   - 报文是逐行 JSON：客户端连接后发一个请求，服务端回一个或多个响应，
//     这样天然支持“订阅日志”这类流式场景，实现也足够简单。
package socket

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ProtocolVersion 是控制协议版本，不匹配时客户端会提示重启 supervisor。
const ProtocolVersion = 1

// DefaultTimeout 是单次请求的默认超时。
const DefaultTimeout = 30 * time.Second

// Endpoint 描述控制通道的接入信息。
type Endpoint struct {
	Version   int               `json:"version"`
	Instance  string            `json:"instance"`
	Network   string            `json:"network"`
	Address   string            `json:"address"`
	Token     string            `json:"token"`
	PID       int               `json:"pid"`
	StartedAt time.Time         `json:"started_at"`
	Binary    string            `json:"binary,omitempty"`
	Extra     map[string]string `json:"extra,omitempty"`
}

// IsZero 判断端点是否为空。
func (e Endpoint) IsZero() bool { return e.Address == "" || e.Token == "" }

// String 返回可读描述。
func (e Endpoint) String() string {
	return fmt.Sprintf("%s(%s@%s pid=%d)", e.Instance, e.Network, e.Address, e.PID)
}

// NewToken 生成随机访问令牌。
func NewToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("生成令牌失败: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// WriteEndpoint 原子写入端点文件（权限 0600）。
func WriteEndpoint(path string, ep Endpoint) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("创建目录 %s 失败: %w", filepath.Dir(path), err)
	}
	data, err := json.MarshalIndent(ep, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("写入端点文件失败: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("替换端点文件失败: %w", err)
	}
	return nil
}

// ReadEndpoint 读取端点文件。
func ReadEndpoint(path string) (Endpoint, error) {
	var ep Endpoint
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ep, ErrNotRunning
		}
		return ep, fmt.Errorf("读取端点文件失败: %w", err)
	}
	if err := json.Unmarshal(data, &ep); err != nil {
		return ep, fmt.Errorf("端点文件损坏: %w", err)
	}
	return ep, nil
}

// RemoveEndpoint 删除端点文件。
func RemoveEndpoint(path string) error {
	err := os.Remove(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// ErrNotRunning 表示目标实例没有在运行。
var ErrNotRunning = errors.New("实例未在运行（找不到控制端点）")

// Request 是一次控制请求。
type Request struct {
	ID    string          `json:"id,omitempty"`
	Token string          `json:"token,omitempty"`
	Cmd   string          `json:"cmd"`
	Args  json.RawMessage `json:"args,omitempty"`
}

// Response 是一次控制响应，Stream 为 true 时表示后面还有数据。
type Response struct {
	ID     string          `json:"id,omitempty"`
	OK     bool            `json:"ok"`
	Error  string          `json:"error,omitempty"`
	Stream bool            `json:"stream,omitempty"`
	Event  string          `json:"event,omitempty"`
	Data   json.RawMessage `json:"data,omitempty"`
}

// Handler 处理一次连接上的请求；可以通过 send 连续推送多个响应（流式）。
type Handler func(ctx context.Context, req Request, send func(Response) error) error

// Server 是控制通道服务端。
type Server struct {
	ep     Endpoint
	ln     net.Listener
	handle Handler

	mu     sync.Mutex
	conns  map[net.Conn]struct{}
	closed bool
	wg     sync.WaitGroup

	onError func(error)
}

// Listen 在回环地址上启动控制通道。
func Listen(ep Endpoint, handle Handler) (*Server, error) {
	if handle == nil {
		return nil, errors.New("handler 不能为空")
	}
	if ep.Network == "" {
		ep.Network = "tcp"
	}
	if ep.Address == "" {
		ep.Address = "127.0.0.1:0"
	}
	if ep.Token == "" {
		token, err := NewToken()
		if err != nil {
			return nil, err
		}
		ep.Token = token
	}
	if ep.Version == 0 {
		ep.Version = ProtocolVersion
	}
	if ep.StartedAt.IsZero() {
		ep.StartedAt = time.Now()
	}

	ln, err := net.Listen(ep.Network, ep.Address)
	if err != nil {
		return nil, fmt.Errorf("监听控制通道 %s 失败: %w", ep.Address, err)
	}
	if ep.Network == "tcp" {
		if !isLoopback(ln.Addr()) {
			_ = ln.Close()
			return nil, fmt.Errorf("控制通道只允许监听回环地址，当前 %s", ln.Addr())
		}
	}
	ep.Address = ln.Addr().String()

	s := &Server{ep: ep, ln: ln, handle: handle, conns: map[net.Conn]struct{}{}}
	go s.accept()
	return s, nil
}

// Endpoint 返回实际使用的端点信息（含真实端口）。
func (s *Server) Endpoint() Endpoint { return s.ep }

// SetErrorHandler 注册连接层错误回调。
func (s *Server) SetErrorHandler(fn func(error)) { s.onError = fn }

func (s *Server) reportError(err error) {
	if err != nil && s.onError != nil {
		s.onError(err)
	}
}

// Close 关闭监听并断开所有连接。
func (s *Server) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	err := s.ln.Close()
	for c := range s.conns {
		_ = c.Close()
	}
	s.mu.Unlock()
	return err
}

func (s *Server) accept() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			s.mu.Lock()
			closed := s.closed
			s.mu.Unlock()
			if !closed {
				s.reportError(err)
			}
			return
		}
		s.mu.Lock()
		s.conns[conn] = struct{}{}
		s.mu.Unlock()
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer func() {
				s.mu.Lock()
				delete(s.conns, conn)
				s.mu.Unlock()
				_ = conn.Close()
			}()
			s.serve(conn)
		}()
	}
}

func (s *Server) serve(conn net.Conn) {
	dec := json.NewDecoder(conn)
	enc := json.NewEncoder(conn)

	var req Request
	if err := dec.Decode(&req); err != nil {
		if !errors.Is(err, io.EOF) {
			s.reportError(fmt.Errorf("读取控制请求失败: %w", err))
		}
		return
	}
	if subtle.ConstantTimeCompare([]byte(req.Token), []byte(s.ep.Token)) != 1 {
		_ = enc.Encode(Response{ID: req.ID, OK: false, Error: "控制令牌无效"})
		return
	}

	send := func(resp Response) error {
		resp.ID = req.ID
		return enc.Encode(resp)
	}

	ctx := context.Background()
	if err := s.handle(ctx, req, send); err != nil {
		_ = send(Response{OK: false, Error: err.Error()})
	}
}

// isLoopback 判断监听地址是否为回环地址。
func isLoopback(addr net.Addr) bool {
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// ---------------------------------------------------------------- 客户端

// Client 是控制通道客户端，一次连接对应一个请求（流式请求会持续读取）。
type Client struct {
	ep   Endpoint
	conn net.Conn
	enc  *json.Encoder
	dec  *json.Decoder
}

// Dial 连接控制通道。
func Dial(ep Endpoint, timeout time.Duration) (*Client, error) {
	if ep.IsZero() {
		return nil, ErrNotRunning
	}
	if ep.Version != 0 && ep.Version != ProtocolVersion {
		return nil, fmt.Errorf("控制协议版本不匹配（服务端 %d，客户端 %d），请重启 supervisor", ep.Version, ProtocolVersion)
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	conn, err := net.DialTimeout(ep.Network, ep.Address, timeout)
	if err != nil {
		return nil, fmt.Errorf("连接 %s 失败: %w", ep.Address, err)
	}
	return &Client{ep: ep, conn: conn, enc: json.NewEncoder(conn), dec: json.NewDecoder(conn)}, nil
}

// Endpoint 返回连接使用的端点。
func (c *Client) Endpoint() Endpoint { return c.ep }

// Close 关闭连接。
func (c *Client) Close() error {
	if c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

func (c *Client) send(ctx context.Context, cmd string, args any) error {
	var raw json.RawMessage
	if args != nil {
		data, err := json.Marshal(args)
		if err != nil {
			return fmt.Errorf("序列化请求参数失败: %w", err)
		}
		raw = data
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = c.conn.SetDeadline(dl)
	} else {
		_ = c.conn.SetDeadline(time.Now().Add(DefaultTimeout))
	}
	req := Request{Token: c.ep.Token, Cmd: cmd, Args: raw}
	if err := c.enc.Encode(req); err != nil {
		return fmt.Errorf("发送控制请求失败: %w", err)
	}
	return nil
}

// Call 发送一次请求并把结果反序列化到 out（out 可为 nil）。
func (c *Client) Call(ctx context.Context, cmd string, args any, out any) error {
	if err := c.send(ctx, cmd, args); err != nil {
		return err
	}
	var resp Response
	if err := c.dec.Decode(&resp); err != nil {
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("supervisor 未响应（可能已退出）")
		}
		return fmt.Errorf("读取控制响应失败: %w", err)
	}
	if !resp.OK {
		if resp.Error == "" {
			resp.Error = "未知错误"
		}
		return errors.New(resp.Error)
	}
	if out != nil && len(resp.Data) > 0 {
		if err := json.Unmarshal(resp.Data, out); err != nil {
			return fmt.Errorf("解析控制响应失败: %w", err)
		}
	}
	return nil
}

// Stream 发送一次请求并持续读取响应，直到服务端关闭或 fn 返回错误。
func (c *Client) Stream(ctx context.Context, cmd string, args any, fn func(Response) error) error {
	if fn == nil {
		return errors.New("流式回调不能为空")
	}
	if err := c.send(ctx, cmd, args); err != nil {
		return err
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = c.conn.SetDeadline(dl)
	} else {
		_ = c.conn.SetDeadline(time.Time{})
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		var resp Response
		if err := c.dec.Decode(&resp); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("读取控制流失败: %w", err)
		}
		if !resp.OK {
			if resp.Error == "" {
				resp.Error = "未知错误"
			}
			return errors.New(resp.Error)
		}
		if err := fn(resp); err != nil {
			return err
		}
	}
}

// Ping 检查控制通道是否可用。
func Ping(ep Endpoint, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	c, err := Dial(ep, timeout)
	if err != nil {
		return err
	}
	defer c.Close()
	return c.Call(ctx, "ping", nil, nil)
}

// PingPath 读取端点文件并探测，失败表示实例未运行。
func PingPath(path string, timeout time.Duration) bool {
	ep, err := ReadEndpoint(path)
	if err != nil {
		return false
	}
	return Ping(ep, timeout) == nil
}

// CallPath 读取端点文件并发起一次调用。
func CallPath(ctx context.Context, path, cmd string, args any, out any) error {
	ep, err := ReadEndpoint(path)
	if err != nil {
		return err
	}
	c, err := Dial(ep, 5*time.Second)
	if err != nil {
		return err
	}
	defer c.Close()
	return c.Call(ctx, cmd, args, out)
}

// StreamPath 读取端点文件并订阅流式响应。
func StreamPath(ctx context.Context, path, cmd string, args any, fn func(Response) error) error {
	ep, err := ReadEndpoint(path)
	if err != nil {
		return err
	}
	c, err := Dial(ep, 5*time.Second)
	if err != nil {
		return err
	}
	defer c.Close()
	return c.Stream(ctx, cmd, args, fn)
}
