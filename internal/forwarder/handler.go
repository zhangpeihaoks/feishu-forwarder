package forwarder

import (
	"context"
	"encoding/json"
	stderrors "errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/zhangpeihaoks/firefly/pkg/errors"
)

// maxBodyBytes 限制入参体积：一屏告警远小于 1MiB，超过就是异常流量。
const maxBodyBytes = 1 << 20

// CardSender 是 Handler 依赖的最小接口，便于测试注入假实现。
type CardSender interface {
	// Send 把卡片发给飞书，失败时返回错误。
	Send(ctx context.Context, card Card) error
}

// Handler 持有配置、发送器与日志，三个路由都是它的方法。
type Handler struct {
	// cfg 是启动时校验过的配置；/readyz 会再校验一次以反映运行时漂移。
	cfg Config
	// sender 是出站发送器。
	sender CardSender
	// logger 是结构化日志。
	logger *slog.Logger
}

// NewHandler 组装 Handler。
func NewHandler(cfg Config, sender CardSender, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{cfg: cfg, sender: sender, logger: logger}
}

// Webhook 处理 Alertmanager 的 POST /webhook[?mention=oncall]。
//
// 返回约定（firefly 会把 *errors.Error 的 Code 作为响应状态码）：
//   - 200：已转给飞书；
//   - 400：载荷不是合法信封或没有 alert（配置/上游错误，需人工看日志）；
//   - 502：飞书拒绝或不可达，让 Alertmanager 记一次失败并在下一轮重试。
func (h *Handler) Webhook(ctx context.Context, req any) (any, error) {
	r, ok := req.(*http.Request)
	if !ok {
		return nil, errors.New(errors.CodeInternal, "BAD_REQUEST_OBJECT", "handler expects *http.Request")
	}

	start := time.Now()
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes))
	if err != nil {
		return nil, errors.New(errors.CodeBadRequest, "READ_BODY", "cannot read request body")
	}

	var msg WebhookMessage
	if err := json.Unmarshal(body, &msg); err != nil {
		return nil, errors.Newf(errors.CodeBadRequest, "INVALID_JSON", "decode payload: %v", err)
	}
	if len(msg.Alerts) == 0 {
		return nil, errors.New(errors.CodeBadRequest, "NO_ALERTS", "payload has no alerts")
	}

	// mention=oncall 是唯一的 @ 开关：由 Alertmanager 的 critical 路由带上，
	// 转发器不再自己判断 severity，避免两处逻辑打架
	mentionIDs := []string(nil)
	if r.URL.Query().Get("mention") == "oncall" {
		mentionIDs = h.cfg.MentionIDs()
	}

	card, err := BuildCard(msg, mentionIDs)
	if err != nil {
		return nil, errors.Newf(errors.CodeInternal, "BUILD_CARD", "build card: %v", err)
	}

	if err := h.sender.Send(ctx, card); err != nil {
		// 记下飞书业务码，排障时不用再去翻出口日志
		code := 0
		var feishuErr *FeishuError
		if stderrors.As(err, &feishuErr) {
			code = feishuErr.Code
		}
		h.logger.Error("forward notification failed",
			"group_key", msg.GroupKey,
			"status", msg.Status,
			"alerts", len(msg.Alerts),
			"mention", len(mentionIDs) > 0,
			"feishu_code", code,
			"err", err,
		)
		return nil, errors.Newf(http.StatusBadGateway, "FEISHU_FAILED", "send to feishu: %v", err)
	}

	h.logger.Info("notification forwarded",
		"group_key", msg.GroupKey,
		"status", msg.Status,
		"alerts", len(msg.Alerts),
		"mention", len(mentionIDs) > 0,
		"duration_ms", time.Since(start).Milliseconds(),
	)
	return map[string]any{"forwarded": len(msg.Alerts)}, nil
}

// Health 是存活探针：进程能响应就算活着，不依赖外部。
func (h *Handler) Health(_ context.Context, _ any) (any, error) {
	return map[string]string{"status": "ok"}, nil
}

// Ready 是就绪探针：配置不全时不接流量，让 Pod 停在未就绪而不是发不出告警。
func (h *Handler) Ready(_ context.Context, _ any) (any, error) {
	if err := h.cfg.Validate(); err != nil {
		return nil, errors.Newf(errors.CodeServiceUnavailable, "NOT_READY", "invalid config: %v", err)
	}
	return map[string]string{"status": "ready"}, nil
}

// MaxBodyBytes 暴露入参体积上限，供 main 里的服务配置与测试共用。
func MaxBodyBytes() int64 {
	return maxBodyBytes
}
