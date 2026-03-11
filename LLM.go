package Seele

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// llmClient 是对 OpenAI 兼容 /v1/chat/completions 的轻量封装。
// 无第三方依赖，纯标准库 net/http。
type llmClient struct {
	cfg    LLMConfig
	client *http.Client
}

func newLLMClient(cfg LLMConfig) *llmClient {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 60
	}
	return &llmClient{
		cfg:    cfg,
		client: &http.Client{Timeout: time.Duration(timeout) * time.Second},
	}
}

// ── 请求 / 响应结构体（仅在本文件使用）────────────────────────────

type chatCompletionRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Tools       []Tool    `json:"tools,omitempty"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
	Temperature float64   `json:"temperature,omitempty"`
}

type chatCompletionResponse struct {
	ID      string `json:"id"`
	Choices []struct {
		Message      Message `json:"message"`
		FinishReason string  `json:"finish_reason"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	} `json:"error"`
}

// ── 核心方法 ──────────────────────────────────────────────────────

// complete 发送一次对话补全请求，返回模型的回复 Message。
//
//   - 若模型发起 tool_calls，Message.ToolCalls 非空，Message.Content 可能为空。
//   - 若模型直接回复，Message.Content 为文本，Message.ToolCalls 为空。
func (c *llmClient) complete(ctx context.Context, messages []Message, tools []Tool) (Message, error) {
	temperature := c.cfg.Temperature
	if temperature == 0 {
		temperature = 1.0
	}

	reqBody := chatCompletionRequest{
		Model:       c.cfg.Model,
		Messages:    messages,
		MaxTokens:   c.cfg.MaxTokens,
		Temperature: temperature,
	}
	if len(tools) > 0 {
		reqBody.Tools = tools
	}

	raw, err := json.Marshal(reqBody)
	if err != nil {
		return Message{}, fmt.Errorf("llmClient: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		c.cfg.BaseURL+"/chat/completions",
		bytes.NewReader(raw),
	)
	if err != nil {
		return Message{}, fmt.Errorf("llmClient: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return Message{}, fmt.Errorf("llmClient: HTTP: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return Message{}, fmt.Errorf("llmClient: read response: %w", err)
	}

	var cr chatCompletionResponse
	if err := json.Unmarshal(data, &cr); err != nil {
		return Message{}, fmt.Errorf("llmClient: parse response: %w\nraw: %.512s", err, data)
	}
	if cr.Error != nil {
		return Message{}, fmt.Errorf("llmClient: API error [%s/%s]: %s",
			cr.Error.Type, cr.Error.Code, cr.Error.Message)
	}
	if len(cr.Choices) == 0 {
		return Message{}, fmt.Errorf("llmClient: empty choices\nraw: %.512s", data)
	}

	return cr.Choices[0].Message, nil
}
