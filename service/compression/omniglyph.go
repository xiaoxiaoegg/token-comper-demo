// service/compression/omniglyph.go
// OmniGlyph 近似：把长文本渲染成图片（上下文转图片，换载体省 token）。
//
// 原理：文本 token 随内容线性增长，而图片 token 是固定的（一张图 N token）。
// 对超长内容，转成图片可能更省——但需要视觉模型消费，且破坏可引用性。
// 这里做「最小可演示」实现：用 freetype 把文本渲染成 PNG，返回 base64 + 元数据。

package compression

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"image"
	"image/png"
	"os"
	"strings"
	"sync"

	"github.com/golang/freetype"
	"github.com/golang/freetype/truetype"
	"golang.org/x/image/math/fixed"
)

// 全局字体（懒加载，线程安全）
var (
	omniglyphFontOnce sync.Once
	omniglyphFont     *truetype.Font
	omniglyphFontErr  error
)

// 候选字体路径（优先中文字体）
var omniglyphFontPaths = []string{
	"/System/Library/Fonts/STHeiti Light.ttc",
	"/System/Library/Fonts/STHeiti Medium.ttc",
	"/System/Library/Fonts/Supplemental/Arial.ttf",
}

// loadOmniglyphFont 懒加载字体
func loadOmniglyphFont() (*truetype.Font, error) {
	omniglyphFontOnce.Do(func() {
		for _, path := range omniglyphFontPaths {
			b, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			f, err := truetype.Parse(b)
			if err != nil {
				continue
			}
			omniglyphFont = f
			return
		}
		omniglyphFontErr = fmt.Errorf("未找到可用字体")
	})
	return omniglyphFont, omniglyphFontErr
}

// OmniGlyphCompressor 文本转图片压缩器
type OmniGlyphCompressor struct{}

// NewOmniGlyphCompressor 创建文本转图片压缩器
func NewOmniGlyphCompressor() *OmniGlyphCompressor { return &OmniGlyphCompressor{} }

func (o *OmniGlyphCompressor) Name() string          { return "omniglyph" }
func (o *OmniGlyphCompressor) Stage() Stage           { return S1 } // 载体切换，内容无损
func (o *OmniGlyphCompressor) Mode() Mode             { return FailOpen }
func (o *OmniGlyphCompressor) OutputKind() OutputKind { return KindStructured }

// IsApplicable 只处理长文本（>200 字符转图才有收益）
func (o *OmniGlyphCompressor) IsApplicable(in *Input) bool {
	if in == nil {
		return false
	}
	s, ok := in.Content.(string)
	return ok && len([]rune(s)) > 200
}

// Apply 把文本渲染成 PNG，返回 base64 图片 + 元数据
func (o *OmniGlyphCompressor) Apply(ctx context.Context, in *Input) (*Output, error) {
	content, ok := in.Content.(string)
	if !ok {
		return FailOpenOutput(in), nil
	}

	f, err := loadOmniglyphFont()
	if err != nil {
		return FailOpenOutput(in), nil
	}

	pngData, err := renderTextToPNG(content, f, 14, 20)
	if err != nil {
		return FailOpenOutput(in), nil
	}
	b64 := base64.StdEncoding.EncodeToString(pngData)

	// 估算：文本 token（~4 字符/token） vs 图片固定 token
	textTokens := len([]rune(content)) / 4
	const imageTokens = 1700 // 一张图固定估算 token
	saved := max0(textTokens - imageTokens)

	out := &Output{
		Content: map[string]any{
			"type":     "image",
			"format":   "png",
			"base64":   b64,
			"note":     "context rendered as image (needs vision model)",
			"text_len": len([]rune(content)),
		},
		SavedTokens: saved,
		OutputKind:  KindStructured,
		Techniques:  []string{"text-to-image", "omniglyph"},
		Metrics: Metrics{
			EngineName:       o.Name(),
			OriginalTokens:   textTokens,
			CompressedTokens: imageTokens,
			SavedTokens:      saved,
		},
	}
	out.Hash = hashContent(content)
	return out, nil
}

// renderTextToPNG 用 freetype 把文本渲染成 PNG（多行）
func renderTextToPNG(text string, f *truetype.Font, fontSize, lineHeight float64) ([]byte, error) {
	lines := strings.Split(text, "\n")

	// 测量：图片宽度 = 最长行宽，高度 = 行数 × 行高
	opts := truetype.Options{Size: fontSize, DPI: 72}
	maxWidth := 0
	for _, line := range lines {
		w := measureTextWidth(f, line, opts)
		if w > maxWidth {
			maxWidth = w
		}
	}
	if maxWidth < 200 {
		maxWidth = 200
	}
	width := maxWidth + 40
	height := len(lines)*int(lineHeight) + 40
	if height < 40 {
		height = 40
	}

	img := image.NewRGBA(image.Rect(0, 0, width, height))
	// 白底
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, image.White)
		}
	}

	c := freetype.NewContext()
	c.SetDPI(72)
	c.SetFont(f)
	c.SetFontSize(fontSize)
	c.SetClip(img.Bounds())
	c.SetDst(img)
	c.SetSrc(image.Black)

	pt := freetype.Pt(20, 20+int(c.PointToFixed(fontSize)>>6))
	for _, line := range lines {
		if line == "" {
			pt.Y += c.PointToFixed(lineHeight)
			continue
		}
		_, err := c.DrawString(line, pt)
		if err != nil {
			return nil, err
		}
		pt.Y += c.PointToFixed(lineHeight)
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// measureTextWidth 测量文本渲染宽度（像素）
func measureTextWidth(f *truetype.Font, text string, opts truetype.Options) int {
	face := truetype.NewFace(f, &opts)
	defer face.Close()
	var w fixed.Int26_6
	for _, r := range text {
		adv, _ := face.GlyphAdvance(r)
		w += adv
	}
	return w.Ceil()
}
