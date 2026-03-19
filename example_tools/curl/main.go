package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os/exec"
	"strconv"
	"strings"

	"github.com/sukasukasuka123/microHub/pb_api"
	pb "github.com/sukasukasuka123/microHub/proto/gen/proto"
	tool "github.com/sukasukasuka123/microHub/root_class/tool"
)

// ── request / response ──────────────────────────────────────────────────────

// CurlRequest 输入参数
type CurlRequest struct {
	Url     string            `json:"url"`      // 请求URL
	Method  string            `json:"method"`   // HTTP方法，默认GET
	Headers map[string]string `json:"headers"`  // 自定义请求头
	Timeout int               `json:"timeout"`  // 超时时间(秒)，0表示无限制
	MaxSize int               `json:"max_size"` // 最大响应大小(字节)，0表示无限制
}

// CurlResponse 输出结果
type CurlResponse struct {
	StatusCode int               `json:"status_code"`     // HTTP状态码
	Body       string            `json:"body"`            // 响应体内容
	Headers    map[string]string `json:"headers"`         // 响应头键值对
	Success    bool              `json:"success"`         // 请求是否成功
	Error      string            `json:"error,omitempty"` // 错误信息（可选）
}

// ── handler ─────────────────────────────────────────────────────────────────

type CurlHandler struct{}

func (h *CurlHandler) ServiceName() string { return "curl" }

func (h *CurlHandler) Execute(req *pb.ToolRequest) (<-chan *pb.ToolResponse, error) {
	var p CurlRequest
	if err := json.Unmarshal(req.Params, &p); err != nil {
		return nil, fmt.Errorf("curl: parse params: %w", err)
	}

	ch := make(chan *pb.ToolResponse, 1)
	go func() {
		defer close(ch)

		result, err := execute(p)
		if err != nil {
			result.Error = err.Error()
			result.Success = false
		} else {
			result.Success = true
		}

		resp, rerr := pb_api.OKResp("curl", req.TaskId, result)
		if rerr != nil {
			ch <- pb_api.ErrorResp("curl", req.TaskId, "BUILD_RESP", rerr.Error(), "")
			return
		}
		ch <- resp
		fmt.Printf("[curl] task=%s url=%q status=%d\n", req.TaskId, p.Url, result.StatusCode)
	}()
	return ch, nil
}

// ── core logic ───────────────────────────────────────────────────────────────

// execute 实现 curl 的核心逻辑
//
// 描述：执行 curl 命令抓取网页内容，支持自定义 HTTP 方法、请求头和超时设置
//
// 逻辑要点：
// - 使用 exec.Command 执行 curl 命令
// - 拼接 -s 静默输出，-i 包含响应头，-X 指定方法，-H 添加请求头，-m 设置超时
// - 使用 --max-filesize 限制响应大小（如系统支持）
// - 捕获输出并解析：状态行、响应头、响应体
// - 处理错误情况并返回友好提示
func execute(p CurlRequest) (CurlResponse, error) {
	resp := CurlResponse{
		Headers: make(map[string]string),
	}

	// 构建 curl 命令参数
	args := []string{"-s", "-i", "-L"} // -s静默，-i包含响应头，-L跟随重定向

	// 指定请求方法（默认GET，curl默认即为GET，非标准方法需显式指定）
	if p.Method != "" && p.Method != "GET" {
		args = append(args, "-X", p.Method)
	}

	// 添加自定义请求头
	for k, v := range p.Headers {
		args = append(args, "-H", fmt.Sprintf("%s: %s", k, v))
	}

	// 设置超时时间（秒）
	if p.Timeout > 0 {
		args = append(args, "-m", strconv.Itoa(p.Timeout))
	}

	// 限制最大响应大小（curl 7.54.0+ 支持 --max-filesize）
	if p.MaxSize > 0 {
		args = append(args, "--max-filesize", strconv.Itoa(p.MaxSize))
	}

	// 添加目标URL（放在最后）
	args = append(args, p.Url)

	// 执行 curl 命令
	cmd := exec.Command("curl", args...)
	output, err := cmd.CombinedOutput()

	// 处理执行错误：部分错误（如超时、文件大小超限）仍可能有有效输出
	if err != nil {
		// 尝试继续解析已有输出，但标记错误
		// 注意：CombinedOutput 在成功时返回 stdout，失败时返回 stderr+stdout
		// curl -i 的输出即使出错也可能包含部分响应
	}

	// 解析 curl -i 输出：响应头 + 空行 + 响应体
	// 格式示例：
	//   HTTP/1.1 200 OK\r\n
	//   Content-Type: text/html\r\n
	//   \r\n
	//   <body>...</body>
	raw := string(output)

	// 处理可能的多个重定向响应：取最后一个完整响应（以最后一个 \r\n\r\n 分割）
	// 简单策略：按 \r\n\r\n 分割，最后两部分是最终响应的 header 和 body
	parts := strings.Split(raw, "\r\n\r\n")
	var headerSection, body string
	if len(parts) >= 2 {
		headerSection = parts[len(parts)-2]
		body = parts[len(parts)-1]
	} else if len(parts) == 1 {
		// 可能只有响应体或格式异常
		headerSection = parts[0]
	}

	// 解析状态行和响应头
	lines := strings.Split(headerSection, "\r\n")
	for i, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// 第一行是状态行：HTTP/1.1 200 OK
		if i == 0 {
			fields := strings.SplitN(line, " ", 3)
			if len(fields) >= 2 {
				if code, err := strconv.Atoi(fields[1]); err == nil {
					resp.StatusCode = code
				}
			}
			continue
		}
		// 其他行是响应头：Key: Value
		if idx := strings.Index(line, ":"); idx > 0 {
			key := strings.TrimSpace(line[:idx])
			value := strings.TrimSpace(line[idx+1:])
			// 处理重复头（简单拼接）
			if existing, ok := resp.Headers[key]; ok {
				resp.Headers[key] = existing + ", " + value
			} else {
				resp.Headers[key] = value
			}
		}
	}

	resp.Body = body

	// 如果状态码为0且无错误，可能是网络层失败
	if resp.StatusCode == 0 && err != nil {
		return resp, fmt.Errorf("request failed: %w", err)
	}

	return resp, nil
}

// ── main ─────────────────────────────────────────────────────────────────────

func main() {
	log.Println("[curl] 启动，监听 :9099")
	if err := tool.New(&CurlHandler{}).Serve(":9099"); err != nil {
		log.Fatalf("[curl] %v", err)
	}
}
