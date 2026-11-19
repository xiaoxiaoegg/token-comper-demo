package logger

import (
    "testing"
)

func TestRedactor(t *testing.T) {
    r := NewRedactor()

    // 测试 1：API Key 脱敏
    text1 := "My key is sk-1234567890abcdefghijklmnop and Bearer abcdefghijklmnopqrstuvwx1234567890"
    redacted1 := r.Redact(text1)
    if redacted1 != "My key is [REDACTED:api_key] and [REDACTED:bearer_token]" {
        t.Errorf("Test 1 Failed: %s", redacted1)
    }

    // 测试 2：身份证校验 (有效身份证脱敏)
    text2 := "ID is 11010519491231002X"
    redacted2 := r.Redact(text2)
    if redacted2 != "ID is [REDACTED:id_card]" {
        t.Errorf("Test 2 Failed: %s", redacted2)
    }

    // 测试 3：身份证校验 (无效身份证不脱敏)
    text3 := "Order num is 110105194912310029" // 末尾校验码不对
    redacted3 := r.Redact(text3)
    if redacted3 != "Order num is 110105194912310029" {
        t.Errorf("Test 3 Failed: %s", redacted3)
    }

    // 测试 4：银行卡 Luhn 校验 (有效卡脱敏，使用了 Visa 官方测试卡号)
    text4 := "Card is 4012888888881881" 
    redacted4 := r.Redact(text4)
    if redacted4 != "Card is [REDACTED:bank_card]" {
        t.Errorf("Test 4 Failed: %s", redacted4)
    }
}