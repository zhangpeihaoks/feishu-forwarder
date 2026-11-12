package forwarder

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	ferrors "github.com/zhangpeihaoks/firefly/pkg/errors"
)

// recordingSender 记录被发送的卡片，并按需返回错误。
type recordingSender struct {
	// cards 是收到的卡片。
	cards []Card
	// err 非空时每次发送都返回它。
	err error
}

// Send 实现 CardSender。
func (s *recordingSender) Send(_ context.Context, card Card) error {
	s.cards = append(s.cards, card)
	return s.err
}

// webhookRequest 构造一个带请求体的 POST。
func webhookRequest(t *testing.T, body, query string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/webhook"+query, strings.NewReader(body))
	return req
}

// validPayload 是一条合法的 Alertmanager 载荷。
const validPayload = `{
  "version": "4",
  "groupKey": "g1",
  "status": "firing",
  "receiver": "feishu-oncall",
  "alerts": [
    {
      "status": "firing",
      "labels": {"alertname": "RedisTargetDown", "severity": "critical", "instance": "redis"},
      "annotations": {"summary": "抓取失败", "description": "连续 2 分钟抓不到"},
      "generatorURL": "http://192.168.31.233:30093/graph"
    }
  ]
}`

// newTestHandler 组装被测 Handler。
func newTestHandler(t *testing.T, cfg Config, sender CardSender) *Handler {
	t.Helper()
	return NewHandler(cfg, sender, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// errorCode 返回错误里带的 HTTP 状态码；非 *errors.Error 时返回 0。
func errorCode(err error) int {
	var typed *ferrors.Error
	if errors.As(err, &typed) {
		return int(typed.Code)
	}
	return 0
}

// TestWebhookForwards 覆盖转发成功与 @ 开关。
func TestWebhookForwards(t *testing.T) {
	tests := []struct {
		name         string
		query        string
		cfg          Config
		wantMentions bool
	}{
		{name: "warning route without mention", query: "", cfg: Config{WebhookURL: "https://example.com/hook"}},
		{
			name:         "critical route mentions on-call",
			query:        "?mention=oncall",
			cfg:          Config{WebhookURL: "https://example.com/hook", OncallOpenIDs: []string{"ou_a"}},
			wantMentions: true,
		},
		{
			name:         "critical route falls back to at all",
			query:        "?mention=oncall",
			cfg:          Config{WebhookURL: "https://example.com/hook", AtAll: true},
			wantMentions: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sender := &recordingSender{}
			h := newTestHandler(t, tt.cfg, sender)

			resp, err := h.Webhook(context.Background(), webhookRequest(t, validPayload, tt.query))
			if err != nil {
				t.Fatalf("Webhook() error = %v", err)
			}
			if resp == nil {
				t.Fatal("Webhook() returned no response body")
			}
			if len(sender.cards) != 1 {
				t.Fatalf("sender got %d cards, want 1", len(sender.cards))
			}
			if sender.cards[0].Mentioned() != tt.wantMentions {
				t.Fatalf("card mentioned = %v, want %v", sender.cards[0].Mentioned(), tt.wantMentions)
			}
		})
	}
}

// TestWebhookErrors 覆盖入参错误与出站失败的映射。
func TestWebhookErrors(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		sendErr  error
		wantCode int
	}{
		{name: "invalid json is 400", body: `{"alerts":`, wantCode: 400},
		{name: "no alerts is 400", body: `{"version":"4","alerts":[]}`, wantCode: 400},
		{
			name:     "feishu rejection is 502 so alertmanager retries",
			body:     validPayload,
			sendErr:  &FeishuError{Code: 19021, Msg: "sign match fail"},
			wantCode: 502,
		},
		{
			name:     "network failure is 502",
			body:     validPayload,
			sendErr:  errors.New("dial tcp: connection refused"),
			wantCode: 502,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sender := &recordingSender{err: tt.sendErr}
			h := newTestHandler(t, Config{WebhookURL: "https://example.com/hook"}, sender)

			_, err := h.Webhook(context.Background(), webhookRequest(t, tt.body, ""))
			if err == nil {
				t.Fatal("expected an error, got nil")
			}
			if got := errorCode(err); got != tt.wantCode {
				t.Fatalf("status code = %d, want %d (err: %v)", got, tt.wantCode, err)
			}
		})
	}
}

// TestReady 覆盖就绪探针：配置不全时返回 503，配好时返回 200 语义。
func TestReady(t *testing.T) {
	tests := []struct {
		name     string
		cfg      Config
		wantCode int
	}{
		{name: "missing webhook url", cfg: Config{}, wantCode: 503},
		{name: "misconfigured scheme", cfg: Config{WebhookURL: "ftp://example.com/hook"}, wantCode: 503},
		{name: "valid config", cfg: Config{WebhookURL: "https://example.com/hook"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newTestHandler(t, tt.cfg, &recordingSender{})
			_, err := h.Ready(context.Background(), nil)
			if tt.wantCode == 0 {
				if err != nil {
					t.Fatalf("Ready() error = %v", err)
				}
				return
			}
			if got := errorCode(err); got != tt.wantCode {
				t.Fatalf("status code = %d, want %d (err: %v)", got, tt.wantCode, err)
			}
		})
	}
}

// TestHealth 确认存活探针不依赖任何配置。
func TestHealth(t *testing.T) {
	h := newTestHandler(t, Config{}, &recordingSender{})
	if _, err := h.Health(context.Background(), nil); err != nil {
		t.Fatalf("Health() error = %v", err)
	}
}

// TestWebhookRequiresHTTPRequest 确认误用（req 不是 *http.Request）会被挡下。
func TestWebhookRequiresHTTPRequest(t *testing.T) {
	h := newTestHandler(t, Config{WebhookURL: "https://example.com/hook"}, &recordingSender{})
	_, err := h.Webhook(context.Background(), "not-a-request")
	if got := errorCode(err); got != 500 {
		t.Fatalf("status code = %d, want 500 (err: %v)", got, err)
	}
}

// TestMaxBodyBytes 确认真实路由能读到 body 上限常量。
func TestMaxBodyBytes(t *testing.T) {
	if MaxBodyBytes() <= 0 {
		t.Fatalf("MaxBodyBytes() = %d, want a positive limit", MaxBodyBytes())
	}
}
