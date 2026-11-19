// Package randutil 封装 crypto/rand 的安全字节生成，供各模块 newID() 使用。
//
// 背景：历史上 10+ 个模块重复"make(16)+rand.Read+hex.Encode"模板，且有两类坏味道：
//   - 部分模块正确处理了错误（panic），另一部分 `_, _ = rand.Read` 吞错
//   - panic 消息各自硬编码模块名，错误处理分散在业务层
//
// 此包提供三个工厂函数：
//   - Hex16()   — 16 字节 hex，各模块 newID() 首选
//   - Bytes(n)  — 任意长度随机字节，返回 error 给调用方
//   - Hex(n)    — 任意长度 hex
//
// crypto/rand.Read 在现代 CPU 上仅当 CSPRNG 熵池耗尽或内核 RNG 子系统不可用时失败
// （极罕见，但必须处理——吞错导致 ID 碰撞风险）。失败时 Hex16() 直接 panic，
// 与历史上各模块的 panic 语义保持一致，且 panic 消息格式统一。
package randutil

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// Hex16 返回 16 字节 CSPRNG 随机字节的 hex 表示（32 字符）。
// crypto/rand 失败时 panic（运行时异常，恢复需要修复系统熵源）。
func Hex16() string {
	b, err := Bytes(16)
	if err != nil {
		panic(fmt.Sprintf("randutil: crypto/rand unavailable: %v", err))
	}
	return hex.EncodeToString(b)
}

// Hex 返回 n 字节 CSPRNG 随机字节的 hex 表示。
// 长度为 0 时返回空字符串；失败时 panic（同 Hex16）。
func Hex(n int) string {
	if n <= 0 {
		return ""
	}
	b, err := Bytes(n)
	if err != nil {
		panic(fmt.Sprintf("randutil: crypto/rand unavailable: %v", err))
	}
	return hex.EncodeToString(b)
}

// Bytes 返回 n 字节 CSPRNG 随机字节，失败时返回 error。
// 调用方若不关心错误（如临时密码里 6 字节熵降级风险极低）可忽略，但 Hex16/Hex 已处理。
func Bytes(n int) ([]byte, error) {
	if n <= 0 {
		return nil, fmt.Errorf("randutil: invalid length %d", n)
	}
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	return b, nil
}
