package forwarder

import (
	"strings"
	"testing"
)

// sampleMessage 构造一轮告警，按用例覆盖字段。
func sampleMessage(status string, alerts ...Alert) WebhookMessage {
	return WebhookMessage{
		Version:  "4",
		GroupKey: "test-group",
		Status:   status,
		Alerts:   alerts,
	}
}

// sampleAlert 构造单条告警。
func sampleAlert(status, name, severity string) Alert {
	return Alert{
		Status: status,
		Labels: map[string]string{
			"alertname": name,
			"severity":  severity,
			"instance":  "redis",
		},
		Annotations: map[string]string{
			"summary":     "概要",
			"description": "描述",
		},
		GeneratorURL: "http://192.168.31.233:30093/graph?g0.expr=up",
	}
}

// TestBuildCard 覆盖配色、标题、正文拼装、@ 与降级所需的 Mentioned。
func TestBuildCard(t *testing.T) {
	tests := []struct {
		name        string
		msg         WebhookMessage
		mentions    []string
		wantErr     bool
		wantTemp    string
		wantTitle   string
		wantParts   []string
		wantAbsent  []string
		wantMention bool
	}{
		{
			name:        "critical firing is red and mentions the on-call",
			msg:         sampleMessage("firing", sampleAlert("firing", "RedisTargetDown", "critical")),
			mentions:    []string{"ou_a"},
			wantTemp:    templateRed,
			wantTitle:   "🔴 告警 · 1 条",
			wantParts:   []string{"**RedisTargetDown**（critical）", "概要", "描述", "实例：redis", "[查看](", `<at id="ou_a"></at>`},
			wantMention: true,
		},
		{
			name:       "warning firing is orange and does not mention",
			msg:        sampleMessage("firing", sampleAlert("firing", "RedisMemoryHigh", "warning")),
			wantTemp:   templateOrange,
			wantTitle:  "🟠 告警 · 1 条",
			wantParts:  []string{"**RedisMemoryHigh**（warning）"},
			wantAbsent: []string{"<at "},
		},
		{
			name:        "resolved is green even for a critical alert",
			msg:         sampleMessage("resolved", sampleAlert("resolved", "RedisTargetDown", "critical")),
			mentions:    []string{"all"},
			wantTemp:    templateGreen,
			wantTitle:   "🟢 已恢复 · 1 条",
			wantParts:   []string{"状态：已恢复", `<at id="all"></at>`},
			wantMention: true,
		},
		{
			name: "every alert of a group is rendered",
			msg: sampleMessage("firing",
				sampleAlert("firing", "RedisTargetDown", "critical"),
				sampleAlert("firing", "RedisKeysEvicted", "critical"),
			),
			wantTemp:  templateRed,
			wantTitle: "🔴 告警 · 2 条",
			wantParts: []string{"**RedisTargetDown**", "**RedisKeysEvicted**"},
		},
		{
			name: "missing generator url produces no empty link",
			msg: sampleMessage("firing", Alert{
				Status:      "firing",
				Labels:      map[string]string{"alertname": "AlertmanagerDown", "severity": "critical"},
				Annotations: map[string]string{"summary": "抓不到 Alertmanager"},
			}),
			wantTemp:   templateRed,
			wantTitle:  "🔴 告警 · 1 条",
			wantParts:  []string{"**AlertmanagerDown**", "抓不到 Alertmanager"},
			wantAbsent: []string{"[查看]"},
		},
		{
			name:    "empty payload is rejected",
			msg:     sampleMessage("firing"),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			card, err := BuildCard(tt.msg, tt.mentions)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected an error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("BuildCard() error = %v", err)
			}
			if card.Header.Template != tt.wantTemp {
				t.Fatalf("template = %q, want %q", card.Header.Template, tt.wantTemp)
			}
			if card.Header.Title.Content != tt.wantTitle {
				t.Fatalf("title = %q, want %q", card.Header.Title.Content, tt.wantTitle)
			}

			body := cardBody(card)
			for _, part := range tt.wantParts {
				if !strings.Contains(body, part) {
					t.Fatalf("body missing %q\nbody:\n%s", part, body)
				}
			}
			for _, absent := range tt.wantAbsent {
				if strings.Contains(body, absent) {
					t.Fatalf("body must not contain %q\nbody:\n%s", absent, body)
				}
			}
			if card.Mentioned() != tt.wantMention {
				t.Fatalf("Mentioned() = %v, want %v", card.Mentioned(), tt.wantMention)
			}
		})
	}
}

// TestCardWithoutMentions 确认降级重发用的副本只去掉 @，正文不动。
func TestCardWithoutMentions(t *testing.T) {
	card, err := BuildCard(sampleMessage("firing", sampleAlert("firing", "RedisTargetDown", "critical")), []string{"ou_a", "ou_b"})
	if err != nil {
		t.Fatalf("BuildCard() error = %v", err)
	}
	if !card.Mentioned() {
		t.Fatal("card should mention before stripping")
	}

	stripped := card.WithoutMentions()
	if stripped.Mentioned() {
		t.Fatal("stripped card still mentions")
	}
	if body := cardBody(stripped); !strings.Contains(body, "**RedisTargetDown**") {
		t.Fatalf("stripped card lost the alert body:\n%s", body)
	}
}

// cardBody 把卡片正文拼起来，便于断言。
func cardBody(card Card) string {
	var b strings.Builder
	for _, el := range card.Elements {
		b.WriteString(el.Text.Content)
		b.WriteString("\n")
	}
	return b.String()
}
