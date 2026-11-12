package forwarder

import (
	"testing"
	"time"
)

// newGetenv 按 map 返回环境变量读取函数，缺失的键返回空串。
func newGetenv(values map[string]string) func(string) string {
	return func(key string) string {
		return values[key]
	}
}

// TestLoadConfig 覆盖默认值、覆盖值与解析失败三种情况。
func TestLoadConfig(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		want    Config
		wantErr bool
	}{
		{
			name: "defaults when only url is set",
			env:  map[string]string{"FEISHU_WEBHOOK_URL": "https://open.feishu.cn/open-apis/bot/v2/hook/x"},
			want: Config{
				WebhookURL:    "https://open.feishu.cn/open-apis/bot/v2/hook/x",
				ListenAddr:    defaultListenAddr,
				FeishuTimeout: defaultFeishuTimeout,
			},
		},
		{
			name: "open ids are split and trimmed",
			env: map[string]string{
				"FEISHU_WEBHOOK_URL":     "https://example.com/hook",
				"FEISHU_ONCALL_OPEN_IDS": " ou_a , ,ou_b ",
				"FEISHU_SIGN_SECRET":     " s3cret ",
				"LISTEN_ADDR":            "127.0.0.1:9000",
				"FEISHU_TIMEOUT":         "2s",
			},
			want: Config{
				WebhookURL:    "https://example.com/hook",
				SignSecret:    "s3cret",
				OncallOpenIDs: []string{"ou_a", "ou_b"},
				ListenAddr:    "127.0.0.1:9000",
				FeishuTimeout: 2 * time.Second,
			},
		},
		{
			name: "at all flag is parsed",
			env: map[string]string{
				"FEISHU_WEBHOOK_URL": "https://example.com/hook",
				"FEISHU_AT_ALL":      "true",
			},
			want: Config{
				WebhookURL:    "https://example.com/hook",
				AtAll:         true,
				ListenAddr:    defaultListenAddr,
				FeishuTimeout: defaultFeishuTimeout,
			},
		},
		{
			name: "invalid boolean is rejected instead of silently false",
			env: map[string]string{
				"FEISHU_WEBHOOK_URL": "https://example.com/hook",
				"FEISHU_AT_ALL":      "yes-please",
			},
			wantErr: true,
		},
		{
			name: "invalid duration is rejected",
			env: map[string]string{
				"FEISHU_WEBHOOK_URL": "https://example.com/hook",
				"FEISHU_TIMEOUT":     "5",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := LoadConfig(newGetenv(tt.env))
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected an error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("LoadConfig() error = %v", err)
			}
			if got.WebhookURL != tt.want.WebhookURL || got.SignSecret != tt.want.SignSecret ||
				got.AtAll != tt.want.AtAll || got.ListenAddr != tt.want.ListenAddr ||
				got.FeishuTimeout != tt.want.FeishuTimeout {
				t.Fatalf("LoadConfig() = %+v, want %+v", got, tt.want)
			}
			if len(got.OncallOpenIDs) != len(tt.want.OncallOpenIDs) {
				t.Fatalf("OncallOpenIDs = %v, want %v", got.OncallOpenIDs, tt.want.OncallOpenIDs)
			}
			for i := range got.OncallOpenIDs {
				if got.OncallOpenIDs[i] != tt.want.OncallOpenIDs[i] {
					t.Fatalf("OncallOpenIDs = %v, want %v", got.OncallOpenIDs, tt.want.OncallOpenIDs)
				}
			}
		})
	}
}

// TestConfigValidate 覆盖 /readyz 与启动阶段共用的必填校验。
func TestConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{name: "https is accepted", url: "https://open.feishu.cn/open-apis/bot/v2/hook/x"},
		{name: "http is accepted for local testing", url: "http://127.0.0.1:8081/hook"},
		{name: "missing url", url: "", wantErr: true},
		{name: "wrong scheme", url: "ftp://example.com/hook", wantErr: true},
		{name: "no host", url: "https://", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Config{WebhookURL: tt.url}.Validate()
			if tt.wantErr && err == nil {
				t.Fatal("expected an error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
		})
	}
}

// TestConfigMentionIDs 覆盖「谁被 @」的三条分支。
func TestConfigMentionIDs(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
		want []string
	}{
		{name: "open ids win", cfg: Config{OncallOpenIDs: []string{"ou_a"}, AtAll: true}, want: []string{"ou_a"}},
		{name: "fallback to all", cfg: Config{AtAll: true}, want: []string{"all"}},
		{name: "nothing configured", cfg: Config{}, want: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.cfg.MentionIDs()
			if len(got) != len(tt.want) {
				t.Fatalf("MentionIDs() = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("MentionIDs() = %v, want %v", got, tt.want)
				}
			}
		})
	}
}
