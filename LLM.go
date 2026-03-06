package Seele

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// LLMConfig 是 LLM 客户端的连接参数。
type LLMConfig struct {
	BaseURL string // 兼容任何 OpenAI 格式的端点（Aliyun、Azure、vLLM 等）
	APIKey  string
	Model   string
	Timeout time.Duration // 默认 60s
}

// llmClient 是无状态的 HTTP 客户端，不持有任何对话历史。
// 多个 Agent 可以安全地共用同一个实例。
type llmClient struct {
	cfg  LLMConfig
	http *http.Client
}

func newLLMClient(cfg LLMConfig) *llmClient {
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	return &llmClient{
		cfg:  cfg,
		http: &http.Client{Timeout: timeout},
	}
}

// chat 发送一次对话请求，返回 LLM 的回复消息。
func (c *llmClient) chat(messages []Message, tools []Tool) (Message, error) {
	req := chatRequest{
		Model:    c.cfg.Model,
		Messages: messages,
		Stream:   false,
	}
	if len(tools) > 0 {
		req.Tools = tools
	}

	body, err := c.post("/chat/completions", req)
	if err != nil {
		return Message{}, err
	}

	var resp chatResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return Message{}, fmt.Errorf("decode response: %w", err)
	}
	if resp.Error != nil {
		return Message{}, fmt.Errorf("llm error [%s]: %s", resp.Error.Type, resp.Error.Message)
	}
	if len(resp.Choices) == 0 {
		return Message{}, fmt.Errorf("llm returned empty choices")
	}
	return resp.Choices[0].Message, nil
}

func (c *llmClient) post(path string, payload interface{}) ([]byte, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, c.cfg.BaseURL+path, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)

	res, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http: %w", err)
	}
	defer res.Body.Close()

	b, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	if res.StatusCode != http.StatusOK {
		var wrap struct {
			Error *apiError `json:"error"`
		}
		if json.Unmarshal(b, &wrap) == nil && wrap.Error != nil {
			return nil, fmt.Errorf("http %d [%s]: %s", res.StatusCode, wrap.Error.Type, wrap.Error.Message)
		}
		return nil, fmt.Errorf("http %d: %s", res.StatusCode, string(b))
	}
	return b, nil
}
