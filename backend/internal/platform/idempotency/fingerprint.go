package idempotency

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
)

// MaxBodyBytes 是参与指纹计算的请求体上限。
//
// 与 HTTP 层的 max_body_bytes 无关：这里防的是"调用方直接绕过中间件传入超大
// 字节切片"的场景。指纹本身是流式的，但解析成 JSON 树会按输入规模占内存，
// 因此设一个与默认请求体上限同量级的硬边界。
const MaxBodyBytes = 1 << 20

// Fingerprint 计算规范化请求体的 SHA-256，返回 64 位小写十六进制。
//
// 规范化规则（ADR-0006 第 2 节）：
//  1. 解析为通用 JSON 树，数字用 json.Number 保留原文，避免大整数经 float64 丢精度；
//  2. 对象键按字节序升序重排，数组保持原序（顺序有语义）；
//  3. 以无多余空白的紧凑形式重新编码，再做 SHA-256。
//
// 因此"字段顺序不同、空白不同"得到同一指纹；`1`、`"1"`、`1.0` 得到不同指纹。
//
// 指纹必须对**原始请求体**计算，而不是对解码后的 DTO：DTO 新增带默认值的字段
// 会让历史键的指纹失配，把合法重试误判为 IDEMPOTENCY_CONFLICT。
func Fingerprint(body []byte) (string, error) {
	if len(body) > MaxBodyBytes {
		return "", fmt.Errorf("%w: 请求体 %d 字节超过上限 %d", ErrInvalidRequest, len(body), MaxBodyBytes)
	}

	dec := json.NewDecoder(bytes.NewReader(body))
	// UseNumber 是关键：默认解码会把数字转成 float64，超过 2^53 的整数会静默
	// 改变值，两个不同的请求体就可能算出相同指纹。
	dec.UseNumber()

	var tree any
	if err := dec.Decode(&tree); err != nil {
		return "", fmt.Errorf("%w: 请求体不是合法 JSON: %v", ErrInvalidRequest, err)
	}
	// 拒绝 `{"a":1} trailing` 这种"合法前缀 + 垃圾后缀"：否则同一键可以对应
	// 不同字节内容而指纹相同。
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("%w: JSON 之后存在多余内容", ErrInvalidRequest)
	}

	var buf bytes.Buffer
	if err := encodeCanonical(&buf, tree); err != nil {
		return "", err
	}
	sum := sha256.Sum256(buf.Bytes())
	return hex.EncodeToString(sum[:]), nil
}

// encodeCanonical 以确定的形式写出 JSON 树：对象键有序、无空白、数字保留原文。
//
// 不使用 json.Marshal 而是手写递归，原因有两个：
//   - encoding/json 对 map 的键排序规则虽然也是升序，但它会把 json.Number
//     当作字符串处理的行为随版本变化，显式处理更稳；
//   - 手写可以拒绝 json.Marshal 会静默接受的值（例如带非法 UTF-16 代理的字符串）。
func encodeCanonical(buf *bytes.Buffer, v any) error {
	switch val := v.(type) {
	case nil:
		buf.WriteString("null")
	case bool:
		if val {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
	case json.Number:
		// 原样写出：`1` 与 `1.0` 在语义上是同一数值，但幂等语义承诺的是
		// "客户端发来的这次请求"，因此保留原文，二者得到不同指纹。
		buf.WriteString(val.String())
	case string:
		// json.Marshal 负责转义引号、控制字符与 HTML 敏感字符，结果确定。
		encoded, err := json.Marshal(val)
		if err != nil {
			return fmt.Errorf("%w: 编码字符串失败: %v", ErrInvalidRequest, err)
		}
		buf.Write(encoded)
	case []any:
		buf.WriteByte('[')
		for i, item := range val {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := encodeCanonical(buf, item); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(val))
		for k := range val {
			keys = append(keys, k)
		}
		// 字节序升序：JSON 键是 UTF-8 字节串，按字节比较与规范中的字典序一致。
		sort.Strings(keys)
		buf.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			encodedKey, err := json.Marshal(k)
			if err != nil {
				return fmt.Errorf("%w: 编码对象键失败: %v", ErrInvalidRequest, err)
			}
			buf.Write(encodedKey)
			buf.WriteByte(':')
			if err := encodeCanonical(buf, val[k]); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
	default:
		// json.Decoder 只会产出上面几种类型；出现其他类型说明实现有误。
		return fmt.Errorf("%w: 无法规范化的 JSON 值 %T", ErrInvalidRequest, v)
	}
	return nil
}

// Key 校验并规范化幂等键。
//
// 长度 8–128、字符集 [A-Za-z0-9._:-]。在进入数据库前拒绝超长或非法值：
// 键会进唯一索引，放任任意长度的输入等于开放一个存储放大面（ADR-0006 第 1 节）。
// 键**不做**大小写折叠：`Abc` 与 `abc` 是两个不同的键，与 HTTP 头的取值语义一致。
func Key(raw string) (string, error) {
	key := strings.TrimSpace(raw)
	if len(key) < minKeyLen || len(key) > maxKeyLen {
		return "", fmt.Errorf("%w: 幂等键长度必须在 %d–%d 之间", ErrInvalidKey, minKeyLen, maxKeyLen)
	}
	for i := 0; i < len(key); i++ {
		if !isKeyChar(key[i]) {
			return "", fmt.Errorf("%w: 幂等键包含非法字符（允许 A-Z a-z 0-9 . _ : -）", ErrInvalidKey)
		}
	}
	return key, nil
}

const (
	minKeyLen = 8
	maxKeyLen = 128
)

// isKeyChar 判断单字节是否属于允许的键字符集。
//
// 逐字节检查而不是遍历 rune：允许集全是 ASCII，任何非 ASCII 字节都非法。
// 按字节判断也让"含多字节字符的键"直接被拒绝，而不是被错误地按字符数接受。
func isKeyChar(b byte) bool {
	switch {
	case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9':
		return true
	case b == '.', b == '_', b == ':', b == '-':
		return true
	default:
		return false
	}
}
