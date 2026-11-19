package middleware

import (
    "bytes"
    "encoding/json"
    "io"
    "strconv"

    "github.com/gin-gonic/gin"
)

type TaskProfile struct {
    MaxTokens       int
    ReasoningEffort string
}

var taskProfiles = map[string]TaskProfile{
    "agent":      {MaxTokens: 4096, ReasoningEffort: "medium"},
    "structured": {MaxTokens: 2048, ReasoningEffort: ""},
    "chat":       {MaxTokens: 1024, ReasoningEffort: ""},
    "reasoning":  {MaxTokens: 8192, ReasoningEffort: "high"},
}

func detectTaskType(body map[string]any) string {
    if tools, ok := body["tools"].([]any); ok && len(tools) > 0 {
        return "agent"
    }
    if rf, ok := body["response_format"].(map[string]any); ok {
        if t, _ := rf["type"].(string); t == "json_object" || t == "json_schema" {
            return "structured"
        }
    }
    if m, _ := body["model"].(string); isReasoningModel(m) {
        return "reasoning"
    }
    return "chat"
}

func isReasoningModel(model string) bool {
    prefixes := []string{"o1", "o3", "o4", "claude-3-thinking", "gemini-2.0-flash-thinking"}
    for _, p := range prefixes {
        if len(model) >= len(p) && model[:len(p)] == p {
            return true
        }
    }
    return false
}

func OutputControl() gin.HandlerFunc {
    return func(c *gin.Context) {
        raw, err := c.GetRawData()
        if err != nil || len(raw) == 0 {
            c.Next()
            return
        }

        var body map[string]any
        if err := json.Unmarshal(raw, &body); err != nil {
            c.Request.Body = io.NopCloser(bytes.NewReader(raw))
            c.Next()
            return
        }

        taskType := detectTaskType(body)
        profile := taskProfiles[taskType]

        if _, exists := body["max_tokens"]; !exists && profile.MaxTokens > 0 {
            body["max_tokens"] = profile.MaxTokens
        }

        if profile.ReasoningEffort != "" {
            if _, exists := body["reasoning_effort"]; !exists {
                body["reasoning_effort"] = profile.ReasoningEffort
            }
        }

        if budget, ok := c.Get("token_budget"); ok {
            if b, ok := budget.(int); ok && b > 0 {
                body["max_tokens"] = b
            }
        }

        newRaw, _ := json.Marshal(body)
        c.Request.Body = io.NopCloser(bytes.NewReader(newRaw))
        c.Request.ContentLength = int64(len(newRaw))
        c.Request.Header.Set("Content-Length", strconv.Itoa(len(newRaw)))

        c.Set("task_type", taskType)
        c.Set("output_max_tokens", profile.MaxTokens)

        c.Next()

        if resp, exists := c.Get("upstream_response"); exists {
            if fr := extractFinishReason(resp); fr == "length" {
                c.Set("finish_reason_length", true)
            }
        }
    }
}

func extractFinishReason(resp any) string {
    m, ok := resp.(map[string]any)
    if !ok {
        return ""
    }
    choices, _ := m["choices"].([]any)
    if len(choices) == 0 {
        return ""
    }
    first, _ := choices[0].(map[string]any)
    fr, _ := first["finish_reason"].(string)
    return fr
}