package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	pb "github.com/sukasukasuka123/microHub/proto/gen/proto"
	tool "github.com/sukasukasuka123/microHub/root_class/tool"
)

// ── request / response ──────────────────────────────────────────────────────

type FetchRequest struct {
	URL     string            `json:"url"`
	Method  string            `json:"method"`  // GET | POST | HEAD，默认 GET
	Headers map[string]string `json:"headers"` // 可选，额外请求头
	Body    string            `json:"body"`    // 可选，POST body
	Limit   int               `json:"limit"`   // 返回字符数上限，0 = 默认 8000
}

type FetchResponse struct {
	URL        string            `json:"url"`
	StatusCode int               `json:"status_code"`
	Headers    map[string]string `json:"headers"`
	Body       string            `json:"body"`
	Truncated  bool              `json:"truncated"`
	Error      string            `json:"error,omitempty"`
}

// ── handler ─────────────────────────────────────────────────────────────────

type FetchHandler struct{}

func (h *FetchHandler) ServiceName() string { return "fetch" }

func (h *FetchHandler) Execute(req *pb.ToolRequest) ([]*pb.ToolResponse, error) {
	var p FetchRequest
	if err := json.Unmarshal(req.Params, &p); err != nil {
		return errResp(h.ServiceName(), fmt.Sprintf("参数解析失败: %v", err))
	}
	if p.URL == "" {
		return errResp(h.ServiceName(), "url 不能为空")
	}
	if p.Method == "" {
		p.Method = "GET"
	}
	if p.Limit <= 0 {
		p.Limit = 8000
	}

	result, fetchErr := doFetch(p)
	if fetchErr != nil {
		result.Error = fetchErr.Error()
	}

	resp, err := tool.NewOKResp(h.ServiceName(), result)
	if err != nil {
		return nil, err
	}
	return []*pb.ToolResponse{resp}, nil
}

// ── http logic ───────────────────────────────────────────────────────────────

func doFetch(p FetchRequest) (FetchResponse, error) {
	client := &http.Client{
		Timeout: 15 * time.Second,
		// 跟随最多 5 次跳转
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("超过最大跳转次数 5")
			}
			return nil
		},
	}

	var bodyReader io.Reader
	if p.Body != "" {
		bodyReader = strings.NewReader(p.Body)
	}

	httpReq, err := http.NewRequest(strings.ToUpper(p.Method), p.URL, bodyReader)
	if err != nil {
		return FetchResponse{URL: p.URL}, fmt.Errorf("构建请求失败: %w", err)
	}

	// 默认 UA，避免被简单屏蔽
	httpReq.Header.Set("User-Agent", "Mozilla/5.0 (compatible; Seele-fetch/1.0)")
	httpReq.Header.Set("Accept", "text/html,application/xhtml+xml,application/json,*/*")
	for k, v := range p.Headers {
		httpReq.Header.Set(k, v)
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		return FetchResponse{URL: p.URL}, fmt.Errorf("请求失败: %w", err)
	}
	defer resp.Body.Close()

	// 读取最多 512KB 原始内容，防止超大页面打爆内存
	limited := io.LimitReader(resp.Body, 512*1024)
	raw, err := io.ReadAll(limited)
	if err != nil {
		return FetchResponse{URL: p.URL, StatusCode: resp.StatusCode}, fmt.Errorf("读取响应失败: %w", err)
	}

	// 收集响应头（只保留常用的）
	useful := []string{"Content-Type", "Content-Length", "Last-Modified", "ETag", "Location"}
	headers := make(map[string]string)
	for _, k := range useful {
		if v := resp.Header.Get(k); v != "" {
			headers[k] = v
		}
	}

	body := string(raw)

	// 字符数截断（按 rune，不截断多字节字符）
	truncated := false
	if utf8.RuneCountInString(body) > p.Limit {
		runes := []rune(body)
		body = string(runes[:p.Limit])
		truncated = true
	}

	return FetchResponse{
		URL:        p.URL,
		StatusCode: resp.StatusCode,
		Headers:    headers,
		Body:       body,
		Truncated:  truncated,
	}, nil
}

// ── helpers ──────────────────────────────────────────────────────────────────

func errResp(name, msg string) ([]*pb.ToolResponse, error) {
	resp, err := tool.NewOKResp(name, FetchResponse{Error: msg})
	if err != nil {
		return nil, err
	}
	return []*pb.ToolResponse{resp}, nil
}

// ── main ─────────────────────────────────────────────────────────────────────

func main() {
	log.Println("[fetch skill]启动，端口：50103")
	tool.New(&FetchHandler{}).Serve(":50103")
}
