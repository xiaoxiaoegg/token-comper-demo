package logger

// logger/logger.go
// 提供脱敏后的日志输出函数：Info / Infof / Error。
// 所有日志在写入前都经过 Redactor 脱敏（PII + 凭据），
// 确保日志里不残留身份证/手机号/银行卡/邮箱/API key 等敏感信息。

import (
	"fmt"
	"log"
)

var globalRedactor *Redactor

// InitRedactor 初始化全局脱敏器（程序启动时调用一次）
func InitRedactor() {
	globalRedactor = NewRedactor()
}

// sanitizeLogMsg 日志写入前脱敏
func sanitizeLogMsg(msg string) string {
	if globalRedactor != nil {
		return globalRedactor.Redact(msg)
	}
	return msg
}

// Info 输出 info 级日志（写入前脱敏）
func Info(msg string) {
	log.Println(sanitizeLogMsg(msg))
}

// Infof 格式化 info 日志（先格式化再脱敏，确保参数内容也被打码）
func Infof(format string, args ...any) {
	log.Println(sanitizeLogMsg(fmt.Sprintf(format, args...)))
}

// Error 输出 error 级日志（写入前脱敏）
func Error(msg string) {
	log.Println("[ERROR] " + sanitizeLogMsg(msg))
}

// Errorf 格式化 error 日志（写入前脱敏）
func Errorf(format string, args ...any) {
	log.Println("[ERROR] " + sanitizeLogMsg(fmt.Sprintf(format, args...)))
}
