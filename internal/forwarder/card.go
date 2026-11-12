package forwarder

import (
	"fmt"
	"strings"
)

// 飞书交互卡片的配色（header.template）。
const (
	templateGreen  = "green"
	templateRed    = "red"
	templateOrange = "orange"
	templateGrey   = "grey"
)

// 卡片元素的标签类型；lark_md 支持加粗、链接与 <at>。
const (
	tagPlainText = "plain_text"
	tagLarkMD    = "lark_md"
	tagDiv       = "div"
)

// Card 是发给飞书自定义机器人的交互卡片（msg_type=interactive 的 card 字段）。
type Card struct {
	// Config 控制卡片布局；宽屏更接近手机端一屏可读。
	Config CardConfig `json:"config"`
	// Header 是标题栏，颜色由告警级别决定。
	Header CardHeader `json:"header"`
	// Elements 是正文，每条告警一段，@ 单独一段。
	Elements []CardElement `json:"elements"`
}

// CardConfig 是卡片配置。
type CardConfig struct {
	// WideScreenMode 打开宽屏模式。
	WideScreenMode bool `json:"wide_screen_mode"`
}

// CardHeader 是卡片标题栏。
type CardHeader struct {
	// Template 是标题栏配色：红=故障、橙=警告、绿=恢复、灰=其它。
	Template string `json:"template"`
	// Title 是标题文本。
	Title CardText `json:"title"`
}

// CardText 是一段文本，Tag 为 plain_text 或 lark_md。
type CardText struct {
	// Tag 决定飞书按纯文本还是 markdown 子集解析。
	Tag string `json:"tag"`
	// Content 是文本内容。
	Content string `json:"content"`
}

// CardElement 是卡片里的一个模块；本服务只用到 div。
type CardElement struct {
	// Tag 是模块类型。
	Tag string `json:"tag"`
	// Text 是模块里的文本。
	Text CardText `json:"text"`
}

// BuildCard 把一轮 Alertmanager 告警渲染成一张卡片。
//
// mentionIDs 非空时追加一段 <at>；open_id 用 all 表示 @所有人。
// 卡片里不放空行：缺失的 summary / description / 链接直接跳过。
func BuildCard(msg WebhookMessage, mentionIDs []string) (Card, error) {
	if len(msg.Alerts) == 0 {
		return Card{}, fmt.Errorf("no alerts in payload")
	}

	card := Card{
		Config: CardConfig{WideScreenMode: true},
		Header: CardHeader{
			Template: cardTemplate(msg),
			Title: CardText{
				Tag:     tagPlainText,
				Content: cardTitle(msg),
			},
		},
	}

	// 逐条渲染：Alertmanager 已经把同一组告警聚合进来了，这里不再二次合并
	for _, alert := range msg.Alerts {
		content := renderAlert(alert)
		if content == "" {
			// 极端情况：没有任何可读字段，至少让收到的人知道是哪条规则
			content = "（该告警没有 summary / description）"
		}
		card.Elements = append(card.Elements, CardElement{
			Tag:  tagDiv,
			Text: CardText{Tag: tagLarkMD, Content: content},
		})
	}

	// @ 只追加一段：飞书的 <at id="..."></at> 在 lark_md 里生效
	if len(mentionIDs) > 0 {
		card.Elements = append(card.Elements, CardElement{
			Tag:  tagDiv,
			Text: CardText{Tag: tagLarkMD, Content: renderMentions(mentionIDs)},
		})
	}

	return card, nil
}

// cardTemplate 选标题栏配色：恢复优先于级别，避免「已恢复」还是红色吓人。
func cardTemplate(msg WebhookMessage) string {
	if msg.Status == "resolved" {
		return templateGreen
	}
	switch msg.HighestSeverity() {
	case severityCritical:
		return templateRed
	case severityWarning:
		return templateOrange
	default:
		return templateGrey
	}
}

// cardTitle 生成「🔴 告警 · 2 条」或「🟢 已恢复 · 1 条」。
func cardTitle(msg WebhookMessage) string {
	label := "告警"
	icon := "🔴"
	switch {
	case msg.Status == "resolved":
		label, icon = "已恢复", "🟢"
	case msg.HighestSeverity() == severityWarning:
		icon = "🟠"
	}
	return fmt.Sprintf("%s %s · %d 条", icon, label, len(msg.Alerts))
}

// renderAlert 把单条告警渲染成 lark_md 文本，空字段跳过不产生空行。
func renderAlert(alert Alert) string {
	var lines []string

	// 标题行：告警名 + 级别，两者都缺时这一行不出现
	name := alert.Labels["alertname"]
	severity := alert.Labels["severity"]
	switch {
	case name != "" && severity != "":
		lines = append(lines, fmt.Sprintf("**%s**（%s）", name, severity))
	case name != "":
		lines = append(lines, fmt.Sprintf("**%s**", name))
	case severity != "":
		lines = append(lines, fmt.Sprintf("**%s**", severity))
	}

	if summary := alert.Annotations["summary"]; summary != "" {
		lines = append(lines, summary)
	}
	if description := alert.Annotations["description"]; description != "" {
		lines = append(lines, description)
	}
	if instance := alert.Labels["instance"]; instance != "" {
		lines = append(lines, fmt.Sprintf("实例：%s", instance))
	}
	// 没有 generatorURL 时不能出现 "[查看]()" 这种空链接
	if alert.GeneratorURL != "" {
		lines = append(lines, fmt.Sprintf("[查看](%s)", alert.GeneratorURL))
	}
	// 单条恢复：标注一下，混在一组里也能区分
	if alert.Status == "resolved" {
		lines = append(lines, "状态：已恢复")
	}

	return strings.Join(lines, "\n")
}

// renderMentions 拼接 <at> 标签；all 表示 @所有人。
func renderMentions(ids []string) string {
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, fmt.Sprintf("<at id=\"%s\"></at>", id))
	}
	return strings.Join(parts, " ")
}

// Mentioned 报告卡片里是否带了 @，供「飞书拒绝 @ 就降级重发」判断。
func (c Card) Mentioned() bool {
	for _, el := range c.Elements {
		if strings.Contains(el.Text.Content, "<at ") {
			return true
		}
	}
	return false
}

// WithoutMentions 返回去掉 @ 段落后的卡片副本。
//
// 用途见 sender.go：被 @ 的人不在群里时飞书可能整体拒绝本条消息，
// 此时宁可少 @ 也要把告警正文送到。
func (c Card) WithoutMentions() Card {
	filtered := make([]CardElement, 0, len(c.Elements))
	for _, el := range c.Elements {
		if strings.Contains(el.Text.Content, "<at ") {
			continue
		}
		filtered = append(filtered, el)
	}
	c.Elements = filtered
	return c
}
