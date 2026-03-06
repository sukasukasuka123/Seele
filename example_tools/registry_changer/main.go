package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	pb "github.com/sukasukasuka123/microHub/proto/gen/proto"
	tool "github.com/sukasukasuka123/microHub/root_class/tool"
)

// ── registry yaml structures ─────────────────────────────────────────────────

type ToolEntry struct {
	Name         string `yaml:"name"`
	Addr         string `yaml:"addr"`
	Method       string `yaml:"method"`
	InputSchema  string `yaml:"input_schema,omitempty"`
	OutputSchema string `yaml:"output_schema,omitempty"`
}

type RegistryFile struct {
	Services struct {
		Tools []ToolEntry `yaml:"tools"`
		Hubs  []struct {
			Name string `yaml:"name"`
			Addr string `yaml:"addr"`
		} `yaml:"hubs"`
	} `yaml:"services"`
	Pool struct {
		GRPCConn struct {
			MinSize int `yaml:"min_size"`
			MaxSize int `yaml:"max_size"`
		} `yaml:"grpc_conn"`
	} `yaml:"pool"`
}

// ── request / response ──────────────────────────────────────────────────────

type RegistryRequest struct {
	Action      string    `json:"action"`       // list | add | remove | update
	Tool        ToolEntry `json:"tool"`         // add/update 时使用
	Name        string    `json:"name"`         // remove/update 时的目标名
	WaitOnline  bool      `json:"wait_online"`  // add 时是否等待端口可达再写入
	WaitTimeout int       `json:"wait_timeout"` // 等待超时秒数，默认 30
}

type RegistryResponse struct {
	Action  string      `json:"action"`
	Tools   []ToolEntry `json:"tools,omitempty"`
	Changed bool        `json:"changed"`
	Error   string      `json:"error,omitempty"`
}

// ── handler ─────────────────────────────────────────────────────────────────

type RegistryHandler struct {
	registryPath string
}

func (h *RegistryHandler) ServiceName() string { return "registry" }

func (h *RegistryHandler) Execute(req *pb.ToolRequest) ([]*pb.ToolResponse, error) {
	var p RegistryRequest
	if err := json.Unmarshal(req.Params, &p); err != nil {
		return errResp(h.ServiceName(), fmt.Sprintf("参数解析失败: %v", err))
	}

	var result RegistryResponse
	var opErr error

	switch p.Action {
	case "list":
		result, opErr = h.list()
	case "add":
		result, opErr = h.add(p)
	case "remove":
		result, opErr = h.remove(p.Name)
	case "update":
		result, opErr = h.update(p)
	default:
		return errResp(h.ServiceName(), fmt.Sprintf("未知 action: %q，支持: list | add | remove | update", p.Action))
	}

	if opErr != nil {
		result.Error = opErr.Error()
	}
	result.Action = p.Action

	resp, err := tool.NewOKResp(h.ServiceName(), result)
	if err != nil {
		return nil, err
	}
	return []*pb.ToolResponse{resp}, nil
}

// ── operations ───────────────────────────────────────────────────────────────

func (h *RegistryHandler) load() (RegistryFile, error) {
	var rf RegistryFile
	data, err := os.ReadFile(h.registryPath)
	if err != nil {
		return rf, fmt.Errorf("读取 registry.yaml 失败: %w", err)
	}
	if err := yaml.Unmarshal(data, &rf); err != nil {
		return rf, fmt.Errorf("解析 registry.yaml 失败: %w", err)
	}
	return rf, nil
}

func (h *RegistryHandler) save(rf RegistryFile) error {
	data, err := yaml.Marshal(rf)
	if err != nil {
		return fmt.Errorf("序列化失败: %w", err)
	}
	return os.WriteFile(h.registryPath, data, 0644)
}

func (h *RegistryHandler) list() (RegistryResponse, error) {
	rf, err := h.load()
	if err != nil {
		return RegistryResponse{}, err
	}
	return RegistryResponse{Tools: rf.Services.Tools}, nil
}

func (h *RegistryHandler) add(p RegistryRequest) (RegistryResponse, error) {
	if p.Tool.Name == "" || p.Tool.Addr == "" {
		return RegistryResponse{}, fmt.Errorf("tool.name 和 tool.addr 不能为空")
	}

	// 等待端口上线
	if p.WaitOnline {
		timeout := p.WaitTimeout
		if timeout <= 0 {
			timeout = 30
		}
		if err := waitPort(p.Tool.Addr, time.Duration(timeout)*time.Second); err != nil {
			return RegistryResponse{}, fmt.Errorf("等待 %s 上线超时: %w", p.Tool.Addr, err)
		}
	}

	rf, err := h.load()
	if err != nil {
		return RegistryResponse{}, err
	}

	// 检查是否已存在
	for _, t := range rf.Services.Tools {
		if t.Name == p.Tool.Name {
			return RegistryResponse{Changed: false}, fmt.Errorf("skill %q 已存在，请用 update", p.Tool.Name)
		}
	}

	rf.Services.Tools = append(rf.Services.Tools, p.Tool)
	if err := h.save(rf); err != nil {
		return RegistryResponse{}, err
	}
	return RegistryResponse{Changed: true, Tools: rf.Services.Tools}, nil
}

func (h *RegistryHandler) remove(name string) (RegistryResponse, error) {
	if name == "" {
		return RegistryResponse{}, fmt.Errorf("name 不能为空")
	}
	rf, err := h.load()
	if err != nil {
		return RegistryResponse{}, err
	}

	original := len(rf.Services.Tools)
	var kept []ToolEntry
	for _, t := range rf.Services.Tools {
		if t.Name != name {
			kept = append(kept, t)
		}
	}
	if len(kept) == original {
		return RegistryResponse{Changed: false}, fmt.Errorf("skill %q 不存在", name)
	}

	rf.Services.Tools = kept
	if err := h.save(rf); err != nil {
		return RegistryResponse{}, err
	}
	return RegistryResponse{Changed: true, Tools: rf.Services.Tools}, nil
}

func (h *RegistryHandler) update(p RegistryRequest) (RegistryResponse, error) {
	target := p.Name
	if target == "" {
		target = p.Tool.Name
	}
	if target == "" {
		return RegistryResponse{}, fmt.Errorf("需要指定 name 或 tool.name")
	}

	rf, err := h.load()
	if err != nil {
		return RegistryResponse{}, err
	}

	found := false
	for i, t := range rf.Services.Tools {
		if t.Name == target {
			// 只更新非零字段
			if p.Tool.Addr != "" {
				rf.Services.Tools[i].Addr = p.Tool.Addr
			}
			if p.Tool.Method != "" {
				rf.Services.Tools[i].Method = p.Tool.Method
			}
			if p.Tool.InputSchema != "" {
				rf.Services.Tools[i].InputSchema = p.Tool.InputSchema
			}
			if p.Tool.OutputSchema != "" {
				rf.Services.Tools[i].OutputSchema = p.Tool.OutputSchema
			}
			found = true
			break
		}
	}
	if !found {
		return RegistryResponse{Changed: false}, fmt.Errorf("skill %q 不存在", target)
	}

	if err := h.save(rf); err != nil {
		return RegistryResponse{}, err
	}
	return RegistryResponse{Changed: true, Tools: rf.Services.Tools}, nil
}

// ── port probe ───────────────────────────────────────────────────────────────

// waitPort 轮询直到 addr 的 TCP 端口可达或超时
func waitPort(addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	// 确保 addr 有端口
	if !strings.Contains(addr, ":") {
		return fmt.Errorf("addr 格式错误，需要 host:port")
	}
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err == nil {
			conn.Close()
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("超时 %s 后端口仍不可达", timeout)
}

// ── helpers ──────────────────────────────────────────────────────────────────

func errResp(name, msg string) ([]*pb.ToolResponse, error) {
	resp, err := tool.NewOKResp(name, RegistryResponse{Error: msg})
	if err != nil {
		return nil, err
	}
	return []*pb.ToolResponse{resp}, nil
}

// ── main ─────────────────────────────────────────────────────────────────────

func main() {
	registryPath := os.Getenv("REGISTRY_PATH")
	if registryPath == "" {
		registryPath = "config/registry.yaml"
	}
	registryPath = filepath.Clean(registryPath)

	tool.New(&RegistryHandler{registryPath: registryPath}).Serve(":50105")
}
