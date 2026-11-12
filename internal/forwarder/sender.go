package forwarder

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// FeishuError 是飞书返回 code != 0 的情况。
//
// 注意 HTTP 200 不代表发送成功：签名错误（19021）、关键词不匹配、机器人被停用
// 都是 200 + 非 0 code。把它当成失败上报，alertmanager_notifications_failed_total
// 才会增长，自监控告警才有意义。
type FeishuError struct {
	// Code 是飞书业务码。
	Code int `json:"code"`
	// Msg 是飞书返回的错误描述。
	Msg string `json:"msg"`
}

// Error 实现 error 接口，英文小写、结尾无标点。
func (e *FeishuError) Error() string {
	return fmt.Sprintf("feishu rejected message: code=%d msg=%s", e.Code, e.Msg)
}

// Sender 负责把卡片发给飞书。
type Sender struct {
	// url 是机器人地址。
	url string
	// secret 是加签 secret，为空表示不加签。
	secret string
	// client 用于出站请求；超时由配置决定，代理走环境变量。
	client *http.Client
}

// NewSender 按配置创建发送器。
func NewSender(cfg Config) *Sender {
	return &Sender{
		url:    cfg.WebhookURL,
		secret: cfg.SignSecret,
		client: &http.Client{Timeout: cfg.FeishuTimeout},
	}
}

// request 是发给飞书的请求体。
type request struct {
	// MsgType 固定 interactive：卡片比纯文本更适合多条告警。
	MsgType string `json:"msg_type"`
	// Card 是卡片内容。
	Card Card `json:"card"`
	// Timestamp 是秒级时间戳，仅加签时出现。
	Timestamp string `json:"timestamp,omitempty"`
	// Sign 是 HMAC 签名，仅加签时出现。
	Sign string `json:"sign,omitempty"`
}

// Send 发送一张卡片。
//
// 失败语义：
//   - 飞书非 2xx、网络错误、超时：直接返回错误（调用方映射成 502，交给
//     Alertmanager 在下一轮 flush 重试——本服务不再重试，避免叠加出重复消息）；
//   - 飞书 200 但 code != 0，且卡片里带过 @：去掉 @ 重发一次后返回结果。
//     这是唯一的重试，因为「被 @ 的人不在群里」会让业务码持续非 0，
//     不降级就永远送不到，比不 @ 更糟。
func (s *Sender) Send(ctx context.Context, card Card) error {
	err := s.sendOnce(ctx, card)
	if err == nil {
		return nil
	}

	var feishuErr *FeishuError
	if !errors.As(err, &feishuErr) || !card.Mentioned() {
		return err
	}
	return s.sendOnce(ctx, card.WithoutMentions())
}

// sendOnce 走一次完整的出站请求。
func (s *Sender) sendOnce(ctx context.Context, card Card) error {
	payload := request{MsgType: "interactive", Card: card}
	// 加签：timestamp 与 sign 都是顶层字段
	if s.secret != "" {
		timestamp := time.Now().Unix()
		sign, err := GenSign(s.secret, timestamp)
		if err != nil {
			return fmt.Errorf("sign request: %w", err)
		}
		payload.Timestamp = strconv.FormatInt(timestamp, 10)
		payload.Sign = sign
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("post to feishu: %w", err)
	}
	// 读完再关，连接才能复用；只留前 4KiB 足够解析控制字段
	defer func() { _ = resp.Body.Close() }()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return fmt.Errorf("read feishu response: %w", err)
	}

	// 非 2xx 视为传输层失败，带上状态码与片段便于排障
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("feishu returned http %d: %s", resp.StatusCode, string(respBody))
	}

	// 2xx 还要看业务码：飞书用 code 表达签名、限流、机器人停用等失败
	var result struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return fmt.Errorf("decode feishu response %q: %w", string(respBody), err)
	}
	if result.Code != 0 {
		return &FeishuError{Code: result.Code, Msg: result.Msg}
	}
	return nil
}
