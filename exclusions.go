// exclusions.go
// 排除规则（Exclusions）：针对 model 名称或 upstream endpoint 配置"跳过/仅无损"压缩。
//
// 对应 OmniRoute 仪表盘「Exclusions / Per-model/endpoint bypass」一栏。
// 典型用途：某些模型自带压缩、或对特定模型/端点强制不压缩（避免破坏协议）。

package main

import (
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
)

// Exclusion 一条排除规则
type Exclusion struct {
	ID        string `json:"id"`
	Type      string `json:"type"`      // "model" | "endpoint"
	Pattern   string `json:"pattern"`   // 子串匹配（小写）
	Action    string `json:"action"`    // "skip"（完全跳过） | "lossless"（仅无损）
	CreatedAt string `json:"created_at"`
}

var exclusionMu sync.RWMutex
var exclusions []Exclusion

// matchExclusion 返回命中 req.Model / endpoint 的第一条规则（无则 nil）
func matchExclusion(model, endpoint string) *Exclusion {
	exclusionMu.RLock()
	defer exclusionMu.RUnlock()
	m := strings.ToLower(model)
	e := strings.ToLower(endpoint)
	for i := range exclusions {
		ex := &exclusions[i]
		p := strings.ToLower(ex.Pattern)
		if p == "" {
			continue
		}
		switch ex.Type {
		case "model":
			if strings.Contains(m, p) {
				return ex
			}
		case "endpoint":
			if strings.Contains(e, p) {
				return ex
			}
		}
	}
	return nil
}

// applyExclusions 根据命中规则调整插件列表（跳过或仅保留无损）
// 返回可能为空插件列表（skip 时）与命中的规则（用于步骤日志）
func applyExclusions(plugins []PluginMeta, model, endpoint string) ([]PluginMeta, *Exclusion) {
	ex := matchExclusion(model, endpoint)
	if ex == nil {
		return plugins, nil
	}
	switch ex.Action {
	case "skip":
		return []PluginMeta{}, ex
	case "lossless":
		kept := plugins[:0]
		for _, p := range plugins {
			if !p.Lossy {
				kept = append(kept, p)
			}
		}
		return kept, ex
	default:
		return plugins, ex
	}
}

// handleListExclusions 列出所有排除规则
func handleListExclusions(c *gin.Context) {
	exclusionMu.RLock()
	defer exclusionMu.RUnlock()
	c.JSON(200, gin.H{"exclusions": exclusions})
}

// handleCreateExclusion 新增一条排除规则
func handleCreateExclusion(c *gin.Context) {
	var req struct {
		Type    string `json:"type"`
		Pattern string `json:"pattern"`
		Action  string `json:"action"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": "invalid body: " + err.Error()})
		return
	}
	if req.Type != "model" && req.Type != "endpoint" {
		c.JSON(400, gin.H{"error": "type 必须为 model 或 endpoint"})
		return
	}
	if req.Action != "skip" && req.Action != "lossless" {
		c.JSON(400, gin.H{"error": "action 必须为 skip 或 lossless"})
		return
	}
	if strings.TrimSpace(req.Pattern) == "" {
		c.JSON(400, gin.H{"error": "pattern 不能为空"})
		return
	}
	ex := Exclusion{
		ID:        "exc-" + randID(),
		Type:      req.Type,
		Pattern:   req.Pattern,
		Action:    req.Action,
		CreatedAt: nowStr(),
	}
	exclusionMu.Lock()
	exclusions = append(exclusions, ex)
	exclusionMu.Unlock()
	c.JSON(200, gin.H{"ok": true, "exclusion": ex})
}

// handleDeleteExclusion 删除一条排除规则
func handleDeleteExclusion(c *gin.Context) {
	id := c.Param("id")
	exclusionMu.Lock()
	defer exclusionMu.Unlock()
	for i, ex := range exclusions {
		if ex.ID == id {
			exclusions = append(exclusions[:i], exclusions[i+1:]...)
			c.JSON(200, gin.H{"ok": true})
			return
		}
	}
	c.JSON(404, gin.H{"error": "exclusion not found"})
}

// randID 简单随机后缀（避免引 crypto，复用 keymanager 的 hex 思路太重，这里用时间戳+计数）
func randID() string {
	exclusionMu.RLock()
	n := len(exclusions)
	exclusionMu.RUnlock()
	return strings.ReplaceAll(nowStr(), ":", "") + "-" + itoa(n+1)
}
