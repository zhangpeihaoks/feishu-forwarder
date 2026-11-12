# feishu-forwarder

把 Prometheus Alertmanager 的 webhook 转成**飞书自定义机器人的交互卡片**：加签、分级 @值班、恢复也发一条。

## 为什么需要它

| 事实 | 后果 |
|---|---|
| Alertmanager 的 `webhook_configs` 只发它自己的 v2 信封（`version`/`groupKey`/`alerts[]`），没有请求体模板 | 与飞书的 `{msg_type, card}` 结构对不上 |
| 飞书「签名校验」要求每个请求带 `timestamp` + `HMAC-SHA256(timestamp+"\n"+secret, 空内容)`，按秒变化 | Alertmanager 不会算 HMAC，`http_config` 只能加静态 header |
| 现成转换器（`XUJiahua/alertmanager-webhook-feishu`、`feiyu563/PrometheusAlert`、`bougou/alertmanager-webhook-adapter`）都不支持飞书加签 | 只能自建这一层 |

所以本服务只做一件事：收 Alertmanager 的信封 → 组卡片 → 加签 → 发飞书。

## 接口

| 方法 + 路径 | 行为 |
|---|---|
| `POST /webhook` | 普通通道：卡片，不 @ |
| `POST /webhook?mention=oncall` | critical 通道：卡片 + @值班 |
| `GET /healthz` | 存活：进程能响应即 200 |
| `GET /readyz` | 就绪：`FEISHU_WEBHOOK_URL` 合法才 200，否则 503 |

响应体用 firefly 的统一信封 `{"code":..,"msg":..,"data":..}`：

- `200`：已转给飞书；
- `400`：载荷不是合法信封 / 没有 `alerts`（配置或上游错误，需人看日志）；
- `502`：飞书拒绝（含 `code != 0`）或不可达。**这是故意的**——Alertmanager 会把它记成一次失败并在下一轮 flush 重试，同时 `alertmanager_notifications_failed_total` 增长，自监控告警才有意义。

## 配置（全部来自环境变量）

| 变量 | 必填 | 说明 |
|---|---|---|
| `FEISHU_WEBHOOK_URL` | 是 | 自定义机器人地址（`https://open.feishu.cn/open-apis/bot/v2/hook/<token>`） |
| `FEISHU_SIGN_SECRET` | 生产必填 | 机器人「签名校验」的 secret；为空表示不加签（仅限本地/联调） |
| `FEISHU_ONCALL_OPEN_IDS` | 否 | 值班人 `open_id`，逗号分隔；多个依次 @ |
| `FEISHU_AT_ALL` | 否 | 没有 `open_id` 时是否退化为 `@所有人`，默认 `false` |
| `LISTEN_ADDR` | 否 | 默认 `:8080` |
| `FEISHU_TIMEOUT` | 否 | 单次出站超时，默认 `5s` |

`open_id` 不是 `user_id`：飞书自定义机器人只认 `open_id`（见[飞书文档](https://open.feishu.cn/document/client-docs/bot-v3/add-custom-bot)）。拿不到时把 `FEISHU_AT_ALL=true` 打开即可退化为 `@所有人`。

## 卡片长什么样

- 标题：`🔴 告警 · 2 条` / `🟢 已恢复 · 1 条`；标题栏配色：critical 红、warning 橙、恢复绿。
- 正文：每个 alert 一段 `lark_md`：`**<alertname>**（severity）`、`summary`、`description`、`实例：<instance>`、`[查看](<generatorURL>)`；空字段跳过，不留空行，没有链接时不出现空链接。
- @：`mention=oncall` 且配了 `FEISHU_ONCALL_OPEN_IDS` 时追加 `<at id="ou_xxx"></at>`。
- 恢复消息与告警走同一通道（Alertmanager 的 `send_resolved: true`），critical 的恢复也会 @值班。

## 失败语义（排障要点）

| 情况 | 行为 |
|---|---|
| 飞书返回 200 但 `code != 0`（如 `19021` 签名错、关键词不匹配、机器人停用） | 当失败：`502` + 日志带 `feishu_code` |
| 卡片带了 @ 且飞书拒绝 | **去掉 @ 重发一次**；仍失败才 `502`。否则「被 @ 的人不在群里」会让 critical 永远送不到 |
| 网络超时 / 5xx | 直接 `502`，不做内部重试（重试交给 Alertmanager，避免叠加出重复消息） |
| 日志 | JSON 到 stdout：成功一行 `notification forwarded`（含 `group_key`/`alerts`/`duration_ms`），失败一行 `forward notification failed`（含 `feishu_code`） |

## 本地运行与测试

```sh
# 单测（含 -race）
go test ./... -race -count=1

# 本地起一个：用一个假的 webhook 地址即可，/readyz 会真实校验配置
FEISHU_WEBHOOK_URL=http://127.0.0.1:8081/hook LISTEN_ADDR=127.0.0.1:8080 go run .
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8080/readyz   # 200
curl -s -X POST http://127.0.0.1:8080/webhook -d @testdata/alertmanager.json  # 400/502，视假地址而定
```

## 镜像

- 发布：推 `vX.Y.Z` tag → `.github/workflows/release.yml` 先跑 `vet` + `-race` 单测，再推 `ghcr.io/zhangpeihaoks/feishu-forwarder:vX.Y.Z` 与 `:latest`。
- 集群清单钉**不可变 tag**（`vX.Y.Z`），`latest` 只给本地开发用。
- 运行身份 10001、根文件系统可只读；镜像自带 `ca-certificates`（HTTPS 必需）与 busybox `wget`（供 k8s `exec` 探针）。

## 依赖

`github.com/zhangpeihaoks/firefly`（MIT）：只用它的 `app` 生命周期、`pkg/server`、`pkg/middleware`、`pkg/errors`。飞书卡片格式、加签与告警语义都在本仓实现，不进框架。
