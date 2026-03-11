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

	"github.com/sukasukasuka123/microHub/pb_api"
	pb "github.com/sukasukasuka123/microHub/proto/gen/proto"
	tool "github.com/sukasukasuka123/microHub/root_class/tool"
)

type FetchRequest struct {
	URL     string            `json:"url"`
	Method  string            `json:"method"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
	Limit   int               `json:"limit"`
}

type FetchResponse struct {
	URL        string            `json:"url"`
	StatusCode int               `json:"status_code"`
	Headers    map[string]string `json:"headers"`
	Body       string            `json:"body"`
	Truncated  bool              `json:"truncated"`
	Error      string            `json:"error,omitempty"`
}

type FetchHandler struct{}

func (h *FetchHandler) ServiceName() string { return "fetch" }

func (h *FetchHandler) Execute(req *pb.ToolRequest) (<-chan *pb.ToolResponse, error) {
	var p FetchRequest
	if err := json.Unmarshal(req.Params, &p); err != nil {
		return nil, fmt.Errorf("fetch: parse params: %w", err)
	}
	if p.URL == "" {
		return nil, fmt.Errorf("fetch: url 不能为空")
	}
	if p.Method == "" {
		p.Method = "GET"
	}
	if p.Limit <= 0 {
		p.Limit = 8000
	}

	ch := make(chan *pb.ToolResponse, 1)
	go func() {
		defer close(ch)

		result, fetchErr := doFetch(p)
		if fetchErr != nil {
			result.Error = fetchErr.Error()
		}

		resp, err := pb_api.OKResp("fetch", req.TaskId, result)
		if err != nil {
			ch <- pb_api.ErrorResp("fetch", req.TaskId, "BUILD_RESP", err.Error(), "")
			return
		}
		ch <- resp
		fmt.Printf("[fetch] task=%s url=%s status=%d\n", req.TaskId, p.URL, result.StatusCode)
	}()
	return ch, nil
}

func doFetch(p FetchRequest) (FetchResponse, error) {
	client := &http.Client{
		Timeout: 15 * time.Second,
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

	limited := io.LimitReader(resp.Body, 512*1024)
	raw, err := io.ReadAll(limited)
	if err != nil {
		return FetchResponse{URL: p.URL, StatusCode: resp.StatusCode}, fmt.Errorf("读取响应失败: %w", err)
	}

	useful := []string{"Content-Type", "Content-Length", "Last-Modified", "ETag", "Location"}
	headers := make(map[string]string)
	for _, k := range useful {
		if v := resp.Header.Get(k); v != "" {
			headers[k] = v
		}
	}

	body := string(raw)
	truncated := false
	if utf8.RuneCountInString(body) > p.Limit {
		body = string([]rune(body)[:p.Limit])
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

func main() {
	log.Println("[fetch] 启动，监听 :50103")
	if err := tool.New(&FetchHandler{}).Serve(":50103"); err != nil {
		log.Fatalf("[fetch] %v", err)
	}
}
