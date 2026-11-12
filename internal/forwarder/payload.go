package forwarder

import "time"

// WebhookMessage 是 Alertmanager webhook 的 v2 信封（只保留转发器用得到的字段）。
//
// 注意它和飞书的 msg_type 信封完全不是一回事：Alertmanager 发的是
// {version, groupKey, status, alerts[...]}，飞书只收
// {msg_type, card|content}，所以必须由本服务改写。
type WebhookMessage struct {
	// Version 是 Alertmanager 的载荷版本，当前固定 "4"。
	Version string `json:"version"`
	// GroupKey 是一轮聚合的唯一键，日志里用它把「一次告警」串起来。
	GroupKey string `json:"groupKey"`
	// Status 取值 firing / resolved，决定卡片的标题与配色。
	Status string `json:"status"`
	// Receiver 是命中的接收器名字（feishu-group / feishu-oncall），仅用于日志。
	Receiver string `json:"receiver"`
	// CommonLabels 与 CommonAnnotations 是这一组的公共标签/注解。
	CommonLabels      map[string]string `json:"commonLabels"`
	CommonAnnotations map[string]string `json:"commonAnnotations"`
	// Alerts 是本轮里的告警条目，卡片逐条渲染。
	Alerts []Alert `json:"alerts"`
}

// Alert 是 Alertmanager 载荷里的单条告警。
type Alert struct {
	// Status 是单条状态（同一组里可能混合 firing 与 resolved）。
	Status string `json:"status"`
	// Labels 里 severity / instance / alertname 是渲染与分级的关键字段。
	Labels map[string]string `json:"labels"`
	// Annotations 里 summary / description 是给人看的内容。
	Annotations map[string]string `json:"annotations"`
	// StartsAt / EndsAt 是告警起止时间。
	StartsAt time.Time `json:"startsAt"`
	EndsAt   time.Time `json:"endsAt"`
	// GeneratorURL 是 Prometheus 的表达式页地址，卡片里的「查看」链接。
	GeneratorURL string `json:"generatorURL"`
	// Fingerprint 是告警指纹，日志里用于去重排查。
	Fingerprint string `json:"fingerprint"`
}

// 告警级别常量，对应规则文件里的 severity 标签。
const (
	severityCritical = "critical"
	severityWarning  = "warning"
)

// HighestSeverity 返回整组里最严重的级别：critical > warning > 其它。
// 卡片配色与标题用它，避免一组里混着 critical 时显示成 warning 的样式。
func (m WebhookMessage) HighestSeverity() string {
	highest := ""
	for _, a := range m.Alerts {
		switch a.Labels["severity"] {
		case severityCritical:
			return severityCritical
		case severityWarning:
			highest = severityWarning
		}
	}
	return highest
}
