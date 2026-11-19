// keymanager.go
// API key 管理（内存版）：每个 key 绑定一套压缩策略。
// 用户持 key 调用网关 → 查 key 的压缩策略 → 应用（被管理员上限钳制）。
//
// demo 阶段用内存 map，重启即清空；后期与 aihub 合并时改持久化。

package main

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// KeyCompression 每个 API key 绑定的压缩策略
type KeyCompression struct {
	Plugins   []string `json:"plugins,omitempty"`   // 用哪些插件
	Intensity string   `json:"intensity,omitempty"` // 强度 lite/full/ultra
	Lossy     bool     `json:"lossy"`               // 是否允许有损（受管理员上限约束）
}

// KeyRecord 一个 API key 的完整记录
type KeyRecord struct {
	Key         string         `json:"key"`
	Name        string         `json:"name"`        // 便于识别（如"数据分析"）
	Compression KeyCompression `json:"compression"` // 绑定的压缩策略
	CreatedAt   string         `json:"created_at"`
}

// KeyManager 内存版 key 管理
type KeyManager struct {
	mu   sync.RWMutex
	keys map[string]KeyRecord // key → 记录
}

// NewKeyManager 创建 key 管理器
func NewKeyManager() *KeyManager {
	return &KeyManager{keys: make(map[string]KeyRecord)}
}

// GenerateKey 生成随机 key（前缀 sk-，便于识别）
func GenerateKey() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "sk-" + hex.EncodeToString(b), nil
}

// Create 生成一个新 key 并绑定压缩策略
func (km *KeyManager) Create(name string, comp KeyCompression) (KeyRecord, error) {
	key, err := GenerateKey()
	if err != nil {
		return KeyRecord{}, err
	}
	rec := KeyRecord{
		Key:         key,
		Name:        name,
		Compression: comp,
		CreatedAt:   nowStr(),
	}
	km.mu.Lock()
	km.keys[key] = rec
	km.mu.Unlock()
	return rec, nil
}

// Get 根据 key 查记录（不存在返回 ok=false）
func (km *KeyManager) Get(key string) (KeyRecord, bool) {
	km.mu.RLock()
	defer km.mu.RUnlock()
	rec, ok := km.keys[key]
	return rec, ok
}

// List 返回所有 key 记录
func (km *KeyManager) List() []KeyRecord {
	km.mu.RLock()
	defer km.mu.RUnlock()
	out := make([]KeyRecord, 0, len(km.keys))
	for _, v := range km.keys {
		out = append(out, v)
	}
	return out
}

// Delete 删除一个 key
func (km *KeyManager) Delete(key string) {
	km.mu.Lock()
	delete(km.keys, key)
	km.mu.Unlock()
}

// Update 更新一个 key 的压缩策略
func (km *KeyManager) Update(key string, comp KeyCompression) bool {
	km.mu.Lock()
	defer km.mu.Unlock()
	rec, ok := km.keys[key]
	if !ok {
		return false
	}
	rec.Compression = comp
	km.keys[key] = rec
	return true
}

// nowStr 当前时间字符串（简单，避免引 time 包太重）
func nowStr() string {
	return time.Now().Format("2006-01-02 15:04:05")
}
