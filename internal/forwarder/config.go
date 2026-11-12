// Package forwarder 把 Alertmanager 的 webhook 转成飞书自定义机器人的交互卡片。
//
// 为什么需要它：Alertmanager 的 webhook_configs 只能发送它自己的 JSON 信封，
// 也没有请求体模板或签名能力；飞书自定义机器人只接受 msg_type 信封，开了
// 「签名校验」后还要求每个请求带上 timestamp 与 HMAC 签名。两边在协议上接不上，
// 所以中间必须有一个转换层，这个包就是那层。
package forwarder

import (
	"cmp"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// 默认值集中在这里，方便 README 与测试引用同一份口径。
const (
	// defaultListenAddr 是未配置 LISTEN_ADDR 时的监听地址；容器里固定 8080。
	defaultListenAddr = ":8080"
	// defaultFeishuTimeout 是单次出站请求的超时；飞书机器人正常在百毫秒级返回。
	defaultFeishuTimeout = 5 * time.Second
)

// Config 是转发器的全部运行期配置。
type Config struct {
	// WebhookURL 是飞书自定义机器人地址（含 hook/<token>），必填。
	WebhookURL string
	// SignSecret 是机器人「签名校验」的 secret；为空表示机器人没开加签。
	SignSecret string
	// OncallOpenIDs 是 critical 要 @ 的人，逗号分隔；为空则不 @（除非 AtAll）。
	OncallOpenIDs []string
	// AtAll 表示没有 OncallOpenIDs 时退化为 @所有人；默认 false，避免误伤。
	AtAll bool
	// ListenAddr 是 HTTP 监听地址。
	ListenAddr string
	// FeishuTimeout 是单次出站请求超时。
	FeishuTimeout time.Duration
}

// LoadConfig 从环境变量读取配置。getenv 便于测试注入。
func LoadConfig(getenv func(string) string) (Config, error) {
	cfg := Config{
		WebhookURL: strings.TrimSpace(getenv("FEISHU_WEBHOOK_URL")),
		SignSecret: strings.TrimSpace(getenv("FEISHU_SIGN_SECRET")),
		ListenAddr: strings.TrimSpace(getenv("LISTEN_ADDR")),
	}

	// 逗号分隔的 open_id 允许空格与空项，方便在 Secret 里排版
	for _, raw := range strings.Split(getenv("FEISHU_ONCALL_OPEN_IDS"), ",") {
		if id := strings.TrimSpace(raw); id != "" {
			cfg.OncallOpenIDs = append(cfg.OncallOpenIDs, id)
		}
	}

	// 布尔值写错就报错而不是静默当 false：这个开关决定 critical 会不会喊人
	if raw := strings.TrimSpace(getenv("FEISHU_AT_ALL")); raw != "" {
		atAll, err := strconv.ParseBool(raw)
		if err != nil {
			return Config{}, fmt.Errorf("parse FEISHU_AT_ALL %q: %w", raw, err)
		}
		cfg.AtAll = atAll
	}

	// 未配置时回落到默认监听地址
	cfg.ListenAddr = cmp.Or(cfg.ListenAddr, defaultListenAddr)

	// 超时同样允许覆盖，非法值直接报错，避免带着坏配置启动
	cfg.FeishuTimeout = defaultFeishuTimeout
	if raw := strings.TrimSpace(getenv("FEISHU_TIMEOUT")); raw != "" {
		timeout, err := time.ParseDuration(raw)
		if err != nil {
			return Config{}, fmt.Errorf("parse FEISHU_TIMEOUT %q: %w", raw, err)
		}
		cfg.FeishuTimeout = timeout
	}

	return cfg, nil
}

// Validate 校验必填项，/readyz 与启动阶段共用同一套判定。
func (c Config) Validate() error {
	if c.WebhookURL == "" {
		return fmt.Errorf("FEISHU_WEBHOOK_URL is required")
	}
	parsed, err := url.Parse(c.WebhookURL)
	if err != nil {
		return fmt.Errorf("parse FEISHU_WEBHOOK_URL: %w", err)
	}
	// 只接受 http(s)：避免把脚本或其他协议塞进出站请求
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("FEISHU_WEBHOOK_URL scheme %q is not http or https", parsed.Scheme)
	}
	if parsed.Host == "" {
		return fmt.Errorf("FEISHU_WEBHOOK_URL has no host")
	}
	return nil
}

// MentionIDs 返回本次请求要 @ 的对象：
// 显式配置了 open_id 就用它们，否则按 AtAll 决定是否 @所有人。
func (c Config) MentionIDs() []string {
	if len(c.OncallOpenIDs) > 0 {
		return c.OncallOpenIDs
	}
	if c.AtAll {
		return []string{"all"}
	}
	return nil
}
