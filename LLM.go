package Seele

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
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

// ── 流式请求结构体 ─────────────────────────────────────────────────

type chatCompletionStreamRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Tools       []Tool    `json:"tools,omitempty"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
	Temperature float64   `json:"temperature,omitempty"`
	Stream      bool      `json:"stream"`
}

// streamDelta 对应一个 SSE data 帧中 choices[0].delta 的字段。
type streamDelta struct {
	Role      string `json:"role,omitempty"`
	Content   string `json:"content,omitempty"`
	ToolCalls []struct {
		Index    int    `json:"index"`
		ID       string `json:"id,omitempty"`
		Type     string `json:"type,omitempty"`
		Function struct {
			Name      string `json:"name,omitempty"`
			Arguments string `json:"arguments,omitempty"`
		} `json:"function"`
	} `json:"tool_calls,omitempty"`
}

type chatCompletionStreamResponse struct {
	Choices []struct {
		Delta        streamDelta `json:"delta"`
		FinishReason string      `json:"finish_reason"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	} `json:"error"`
}

// ── completeStream ────────────────────────────────────────────────

// completeStream 发起流式 chat completion 请求。
//
// 行为规则（与 OpenAI 流式协议对齐）：
//   - 若模型返回纯文本：每个 content delta 同步调用 onChunk；
//     最终返回 (完整文本, nil toolCalls, nil)
//   - 若模型返回 tool_calls：不调用 onChunk，累积所有 delta 后
//     返回 ("", toolCalls, nil)
//
// 调用方无需区分两种情况，只需检查返回的 toolCalls 是否为空。
func (c *llmClient) completeStream(
	ctx context.Context,
	messages []Message,
	tools []Tool,
	onChunk func(delta string),
) (content string, toolCalls []ToolCall, err error) {

	temperature := c.cfg.Temperature
	if temperature == 0 {
		temperature = 1.0
	}

	reqBody := chatCompletionStreamRequest{
		Model:       c.cfg.Model,
		Messages:    messages,
		MaxTokens:   c.cfg.MaxTokens,
		Temperature: temperature,
		Stream:      true,
	}
	if len(tools) > 0 {
		reqBody.Tools = tools
	}

	raw, err := json.Marshal(reqBody)
	if err != nil {
		return "", nil, fmt.Errorf("llmClient stream: marshal: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(
		ctx, http.MethodPost,
		c.cfg.BaseURL+"/chat/completions",
		bytes.NewReader(raw),
	)
	if err != nil {
		return "", nil, fmt.Errorf("llmClient stream: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return "", nil, fmt.Errorf("llmClient stream: HTTP: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", nil, fmt.Errorf("llmClient stream: HTTP %d: %.512s", resp.StatusCode, body)
	}

	// tool_calls 累积表：index → 对应的 ToolCall（Arguments 逐帧拼接）
	tcMap := make(map[int]*ToolCall)
	var sb strings.Builder
	isToolMode := false

	reader := bufio.NewReader(resp.Body)
	for {
		line, readErr := reader.ReadString('\n')
		line = strings.TrimRight(line, "\r\n")

		switch {
		case line == "data: [DONE]":
			// 流结束
			goto done

		case strings.HasPrefix(line, "data: "):
			payload := strings.TrimPrefix(line, "data: ")
			if payload == "" {
				break
			}

			var frame chatCompletionStreamResponse
			if jsonErr := json.Unmarshal([]byte(payload), &frame); jsonErr != nil {
				// 跳过无法解析的帧，不中断流
				break
			}
			if frame.Error != nil {
				return "", nil, fmt.Errorf("llmClient stream: API error [%s/%s]: %s",
					frame.Error.Type, frame.Error.Code, frame.Error.Message)
			}
			if len(frame.Choices) == 0 {
				break
			}

			delta := frame.Choices[0].Delta

			// ── tool_calls 帧 ──────────────────────────────────────
			if len(delta.ToolCalls) > 0 {
				isToolMode = true
				for _, tc := range delta.ToolCalls {
					entry, exists := tcMap[tc.Index]
					if !exists {
						entry = &ToolCall{Type: "function"}
						tcMap[tc.Index] = entry
					}
					// 首帧携带 ID 和函数名
					if tc.ID != "" {
						entry.ID = tc.ID
					}
					if tc.Function.Name != "" {
						entry.Function.Name = tc.Function.Name
					}
					// Arguments 是流式 JSON 碎片，逐帧拼接
					entry.Function.Arguments += tc.Function.Arguments
				}
			}

			// ── 文本内容帧 ────────────────────────────────────────
			if !isToolMode && delta.Content != "" {
				sb.WriteString(delta.Content)
				onChunk(delta.Content)
			}
		}

		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			return "", nil, fmt.Errorf("llmClient stream: read SSE: %w", readErr)
		}
	}

done:
	if isToolMode {
		// 按 index 顺序整理 tool_calls
		result := make([]ToolCall, len(tcMap))
		for idx, tc := range tcMap {
			if idx < len(result) {
				result[idx] = *tc
			}
		}
		return "", result, nil
	}
	return sb.String(), nil, nil
}
