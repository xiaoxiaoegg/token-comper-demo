// upstream/client.go
// 上游模型调用：把压缩后的请求转发给真实模型（火山方舟 coding plan）。
//
// 安全：API key 从环境变量读取，绝不硬编码；请求体透传给上游。

package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// Client 上游模型客户端
type Client struct {
	BaseURL string
	Model   string
	APIKey  string
	HTTP    *http.Client
}

// Config 上游配置
type Config struct {
	BaseURL string
	Model   string
	APIKey  string
}

// FromEnv 从环境变量读取上游配置
func FromEnv() (*Client, error) {
	cfg := Config{
		BaseURL: os.Getenv("UPSTREAM_BASE_URL"),
		Model:   os.Getenv("UPSTREAM_MODEL"),
		APIKey:  os.Getenv("UPSTREAM_API_KEY"),
	}
	if cfg.BaseURL == "" {
		return nil, fmt.Errorf("UPSTREAM_BASE_URL 未配置")
	}
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("UPSTREAM_API_KEY 未配置")
	}
	return New(cfg), nil
}

// New 创建上游客户端
func New(cfg Config) *Client {
	return &Client{
		BaseURL: cfg.BaseURL,
		Model:   cfg.Model,
		APIKey:  cfg.APIKey,
		HTTP:    &http.Client{Timeout: 120 * time.Second},
	}
}

// ChatCompletionRequest 转发给上游的请求体（OpenAI 兼容）
type ChatCompletionRequest struct {
	Model       string         `json:"model"`
	Messages    []Message      `json:"messages"`
	Tools       []map[string]any `json:"tools,omitempty"`
	MaxTokens   *int           `json:"max_tokens,omitempty"`
	Temperature *float64       `json:"temperature,omitempty"`
	Stream      bool           `json:"stream,omitempty"`
}

// Message 一条消息
type Message struct {
	Role       string           `json:"role"`
	Content    any              `json:"content"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
	ToolCalls  []map[string]any `json:"tool_calls,omitempty"`
}

// ChatCompletionResponse 上游响应（OpenAI 兼容）
type ChatCompletionResponse struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Index        int `json:"index"`
		Message      struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *Usage `json:"usage,omitempty"`
}

// Usage 用量统计
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// Chat 调用上游模型（非流式）
// 输入已压缩的请求体，返回上游真实响应。
// 对网络错误 / 429 / 5xx 自动重试（指数退避），4xx（非 429）视为客户端错误直接返回。
func (c *Client) Chat(ctx context.Context, req ChatCompletionRequest) (*ChatCompletionResponse, error) {
	// 请求体里 model 用配置的模型（如果调用方没指定）
	if req.Model == "" {
		req.Model = c.Model
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	const maxAttempts = 3
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			// 指数退避：200ms / 400ms
			backoff := time.Duration(200*(1<<(attempt-1))) * time.Millisecond
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff):
			}
		}

		chatResp, retryable, err := c.doOnce(ctx, body)
		if err == nil {
			return chatResp, nil
		}
		lastErr = err
		if !retryable || attempt == maxAttempts-1 {
			break
		}
	}
	return nil, lastErr
}

// doOnce 执行单次上游调用；返回是否可重试（网络错误 / 429 / 5xx）
func (c *Client) doOnce(ctx context.Context, body []byte) (*ChatCompletionResponse, bool, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, false, fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.APIKey)

	resp, err := c.HTTP.Do(httpReq)
	if err != nil {
		// 网络错误可重试
		return nil, true, fmt.Errorf("call upstream: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, true, fmt.Errorf("read upstream response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		retryable := resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
		return nil, retryable, fmt.Errorf("upstream error %d: %s", resp.StatusCode, string(respBody))
	}

	var chatResp ChatCompletionResponse
	if err := json.Unmarshal(respBody, &chatResp); err != nil {
		return nil, false, fmt.Errorf("parse upstream response: %w", err)
	}
	return &chatResp, false, nil
}
