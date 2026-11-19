// logger/redact.go
package logger

// 这个文件定义了 Redactor 脱敏器，负责对文本进行 PII 和凭据脱敏,提供一个独立的 Redactor，这样日志层和压缩前的原文处理都可以调用它。

import (
    "regexp"
    "strconv"
    "strings"
)

// Redactor 负责对文本进行 PII 和凭据脱敏
type Redactor struct {
    patterns []redactPattern
}

type redactPattern struct {
    name    string
    regex   *regexp.Regexp
    replace string
    // verifyFunc 用于需要算法校验的场景（如银行卡 Luhn、身份证 GB11643）
    // 返回 true 表示匹配项是有效的，需要被替换
    verifyFunc func(match string) bool
}

func NewRedactor() *Redactor {
    r := &Redactor{}

    // 1. 凭据类（直接正则替换，无需校验）
    r.patterns = append(r.patterns, redactPattern{
        name:    "api_key",
        regex:   regexp.MustCompile(`\b(sk-[a-zA-Z0-9]{20,}|sk-ant-[a-zA-Z0-9-_]{20,}|AKIA[0-9A-Z]{16}|xox[baprs]-[a-zA-Z0-9-]+)\b`),
        replace: "[REDACTED:api_key]",
    })
    r.patterns = append(r.patterns, redactPattern{
        name:    "bearer_token",
        regex:   regexp.MustCompile(`(?i)\bBearer\s+[a-zA-Z0-9-_\.]{20,}\b`),
        replace: "[REDACTED:bearer_token]",
    })
    r.patterns = append(r.patterns, redactPattern{
        name:    "private_key",
        regex:   regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z ]*PRIVATE KEY-----`),
        replace: "[REDACTED:private_key]",
    })
    r.patterns = append(r.patterns, redactPattern{
        name:    "connection_string",
        regex:   regexp.MustCompile(`(?i)(password|pwd|secret|token)\s*=\s*[^\s;&'"]+`),
        replace: "[REDACTED:credential]",
    })

    // 2. PII 类（直接正则替换）
    r.patterns = append(r.patterns, redactPattern{
        name:    "email",
        regex:   regexp.MustCompile(`[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}`),
        replace: "[REDACTED:email]",
    })
    r.patterns = append(r.patterns, redactPattern{
        name:    "phone",
        regex:   regexp.MustCompile(`\b1[3-9]\d{9}\b`), // 中国大陆手机号
        replace: "[REDACTED:phone]",
    })
    r.patterns = append(r.patterns, redactPattern{
        name:    "ipv4",
        regex:   regexp.MustCompile(`\b(?:(?:25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?)\.){3}(?:25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?)\b`),
        replace: "[REDACTED:ipv4]",
    })

    // 3. PII 类（需要算法校验，避免误杀普通数字串）
    r.patterns = append(r.patterns, redactPattern{
        name:    "id_card",
        regex:   regexp.MustCompile(`\b[1-9]\d{5}(?:19|20)\d{2}(?:0[1-9]|1[0-2])(?:0[1-9]|[12]\d|3[01])\d{3}[\dXx]\b`),
        replace: "[REDACTED:id_card]",
        verifyFunc: isValidIDCard,
    })
    r.patterns = append(r.patterns, redactPattern{
        name:    "bank_card",
        regex:   regexp.MustCompile(`\b\d{13,19}\b`),
        replace: "[REDACTED:bank_card]",
        verifyFunc: isValidBankCard,
    })

    return r
}

// Redact 对文本进行脱敏处理
func (r *Redactor) Redact(text string) string {
    for _, p := range r.patterns {
        if p.verifyFunc != nil {
            // 需要校验的，逐个匹配检查
            matches := p.regex.FindAllString(text, -1)
            for _, match := range matches {
                if p.verifyFunc(match) {
                    text = strings.ReplaceAll(text, match, p.replace)
                }
            }
        } else {
            // 不需要校验的，直接全局替换
            text = p.regex.ReplaceAllString(text, p.replace)
        }
    }
    return text
}

// isValidIDCard 校验身份证（GB11643）
func isValidIDCard(id string) bool {
    if len(id) != 18 {
        return false
    }
    // 前17位必须是数字
    for i := 0; i < 17; i++ {
        if id[i] < '0' || id[i] > '9' {
            return false
        }
    }
    // 第18位是数字或X
    last := id[17]
    if !(last >= '0' && last <= '9') && last != 'X' && last != 'x' {
        return false
    }

    // 加权因子
    weights := []int{7, 9, 10, 5, 8, 4, 2, 1, 6, 3, 7, 9, 10, 5, 8, 4, 2}
    // 校验码对应值
    checkCodes := []byte{'1', '0', 'X', '9', '8', '7', '6', '5', '4', '3', '2'}

    sum := 0
    for i := 0; i < 17; i++ {
        digit, _ := strconv.Atoi(string(id[i]))
        sum += digit * weights[i]
    }

    mod := sum % 11
    expectedCheckCode := checkCodes[mod]
    // 大小写兼容
    return (last == expectedCheckCode) || (last == 'x' && expectedCheckCode == 'X')
}

// isValidBankCard 校验银行卡（Luhn 算法）
func isValidBankCard(cardNum string) bool {
    sum := 0
    isSecond := false
    for i := len(cardNum) - 1; i >= 0; i-- {
        digit, err := strconv.Atoi(string(cardNum[i]))
        if err != nil {
            return false
        }
        if isSecond {
            digit *= 2
            if digit > 9 {
                digit -= 9
            }
        }
        sum += digit
        isSecond = !isSecond
    }
    return sum%10 == 0
}