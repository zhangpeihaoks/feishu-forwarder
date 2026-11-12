package forwarder

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// capturedRequest 是假飞书收到的请求体。
type capturedRequest struct {
	MsgType   string `json:"msg_type"`
	Timestamp string `json:"timestamp"`
	Sign      string `json:"sign"`
	Card      Card   `json:"card"`
}

// fakeFeishu 启动一个按顺序返回预设响应的假飞书，返回配置好的 Sender 与收到的请求。
func fakeFeishu(t *testing.T, responses ...string) (*Sender, *[]capturedRequest) {
	t.Helper()

	var (
		mu       sync.Mutex
		received []capturedRequest
		call     int
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req capturedRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		mu.Lock()
		received = append(received, req)
		index := call
		call++
		mu.Unlock()

		// 用完后重复最后一条，避免用例数目与响应数目不一致时报下标越界
		if index >= len(responses) {
			index = len(responses) - 1
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(responses[index]))
	}))
	t.Cleanup(server.Close)

	cfg := Config{WebhookURL: server.URL, FeishuTimeout: 2 * time.Second}
	return NewSender(cfg), &received
}

// mustCard 构造一张带或不带 @ 的卡片。
func mustCard(t *testing.T, mentions []string) Card {
	t.Helper()
	card, err := BuildCard(sampleMessage("firing", sampleAlert("firing", "RedisTargetDown", "critical")), mentions)
	if err != nil {
		t.Fatalf("BuildCard() error = %v", err)
	}
	return card
}

// TestSenderSend 覆盖成功、签名、业务码失败与「去 @ 降级」。
func TestSenderSend(t *testing.T) {
	tests := []struct {
		name        string
		responses   []string
		secret      string
		mentions    []string
		wantErr     bool
		wantFeishu  int
		wantCalls   int
		wantNoAtOn2 bool
	}{
		{
			name:      "success",
			responses: []string{`{"code":0,"msg":"success"}`},
			wantCalls: 1,
		},
		{
			name:      "signed request carries timestamp and sign",
			responses: []string{`{"code":0}`},
			secret:    "demo-secret",
			wantCalls: 1,
		},
		{
			name:       "business code is an error",
			responses:  []string{`{"code":19021,"msg":"sign match fail"}`},
			wantErr:    true,
			wantFeishu: 19021,
			wantCalls:  1,
		},
		{
			name:        "mention rejected then retried without it",
			responses:   []string{`{"code":19024,"msg":"at user not in chat"}`, `{"code":0}`},
			mentions:    []string{"ou_missing"},
			wantCalls:   2,
			wantNoAtOn2: true,
		},
		{
			name:       "mention rejected twice stays an error",
			responses:  []string{`{"code":19024,"msg":"at user not in chat"}`, `{"code":19024,"msg":"at user not in chat"}`},
			mentions:   []string{"ou_missing"},
			wantErr:    true,
			wantFeishu: 19024,
			wantCalls:  2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sender, received := fakeFeishu(t, tt.responses...)
			// 加签场景单独注入 secret（NewSender 从 Config 读取）
			if tt.secret != "" {
				sender.secret = tt.secret
			}

			card := mustCard(t, tt.mentions)
			err := sender.Send(context.Background(), card)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected an error, got nil")
				}
				var feishuErr *FeishuError
				if tt.wantFeishu != 0 && !errors.As(err, &feishuErr) {
					t.Fatalf("error = %v, want *FeishuError", err)
				}
				if tt.wantFeishu != 0 && feishuErr.Code != tt.wantFeishu {
					t.Fatalf("feishu code = %d, want %d", feishuErr.Code, tt.wantFeishu)
				}
			} else if err != nil {
				t.Fatalf("Send() error = %v", err)
			}

			if len(*received) != tt.wantCalls {
				t.Fatalf("feishu received %d calls, want %d", len(*received), tt.wantCalls)
			}
			first := (*received)[0]
			if first.MsgType != "interactive" {
				t.Fatalf("msg_type = %q, want interactive", first.MsgType)
			}
			if tt.secret == "" {
				if first.Timestamp != "" || first.Sign != "" {
					t.Fatalf("unsigned request must not carry timestamp/sign, got %q/%q", first.Timestamp, first.Sign)
				}
			} else if first.Timestamp == "" || first.Sign == "" {
				t.Fatal("signed request is missing timestamp or sign")
			}

			if tt.wantNoAtOn2 {
				second := (*received)[1]
				if second.Card.Mentioned() {
					t.Fatal("retry still contains an @")
				}
				if !strings.Contains(cardBody(second.Card), "**RedisTargetDown**") {
					t.Fatal("retry lost the alert body")
				}
			}
		})
	}
}

// TestSenderHTTPFailure 覆盖非 2xx：传输层失败不重试、不解析业务码。
func TestSenderHTTPFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("upstream down"))
	}))
	defer server.Close()

	sender := NewSender(Config{WebhookURL: server.URL, FeishuTimeout: time.Second})
	err := sender.Send(context.Background(), mustCard(t, nil))
	if err == nil {
		t.Fatal("expected an error for a non-2xx response")
	}
	var feishuErr *FeishuError
	if errors.As(err, &feishuErr) {
		t.Fatal("transport failure must not be reported as a business-code error")
	}
}

// TestSenderTimeout 确认出站超时会被如实返回。
func TestSenderTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		_, _ = w.Write([]byte(`{"code":0}`))
	}))
	defer server.Close()

	sender := NewSender(Config{WebhookURL: server.URL, FeishuTimeout: 20 * time.Millisecond})
	if err := sender.Send(context.Background(), mustCard(t, nil)); err == nil {
		t.Fatal("expected a timeout error")
	}
}
