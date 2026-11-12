package forwarder

import (
	"encoding/base64"
	"testing"
)

// TestGenSign 覆盖加签的三个契约：固定向量、空 secret、时间戳敏感。
func TestGenSign(t *testing.T) {
	tests := []struct {
		name      string
		secret    string
		timestamp int64
		// want 是独立实现（Python hmac）算出的期望值，用来防止改坏算法还自洽
		want string
	}{
		{
			name:      "known vector",
			secret:    "demo-secret",
			timestamp: 1700000000,
			want:      "mYHw2R2SOm8Rw/sne3lQdmz4sOfntKR+1P/8RKKTwmA=",
		},
		{
			name:      "empty secret means unsigned",
			secret:    "",
			timestamp: 1700000000,
			want:      "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := GenSign(tt.secret, tt.timestamp)
			if err != nil {
				t.Fatalf("GenSign() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("GenSign() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestGenSignIsTimeSensitive 确认签名随时间戳变化：飞书用它防止重放。
func TestGenSignIsTimeSensitive(t *testing.T) {
	first, err := GenSign("demo-secret", 1700000000)
	if err != nil {
		t.Fatalf("GenSign() error = %v", err)
	}
	second, err := GenSign("demo-secret", 1700000001)
	if err != nil {
		t.Fatalf("GenSign() error = %v", err)
	}
	if first == second {
		t.Fatal("signature did not change with the timestamp")
	}

	// 顺带确认形状：base64 解出来必须是 32 字节的 SHA-256 摘要
	raw, err := base64.StdEncoding.DecodeString(first)
	if err != nil {
		t.Fatalf("signature is not valid base64: %v", err)
	}
	if len(raw) != 32 {
		t.Fatalf("digest length = %d, want 32", len(raw))
	}
}
