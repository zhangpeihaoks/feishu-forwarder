// feishu-forwarder 把 Alertmanager 的 webhook 转成飞书自定义机器人的交互卡片。
//
// 形态：无状态单副本 HTTP 服务，跑在集群内，只接受 Alertmanager 的调用。
// 依赖：github.com/zhangpeihaoks/firefly 的 app 生命周期与 pkg/server 公开面。
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/zhangpeihaoks/firefly/app"
	fmiddleware "github.com/zhangpeihaoks/firefly/pkg/middleware"
	fserver "github.com/zhangpeihaoks/firefly/pkg/server"

	"github.com/zhangpeihaoks/feishu-forwarder/internal/forwarder"
)

// version 与 commit 由构建时的 -ldflags 注入，便于在日志里确认线上跑的是哪一版。
var (
	version = "dev"
	commit  = "none"
)

func main() {
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Printf("feishu-forwarder %s (%s)\n", version, commit)
		return
	}

	// JSON 日志便于集群侧采集；级别固定 info，排障时可换成 debug
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := forwarder.LoadConfig(os.Getenv)
	if err != nil {
		logger.Error("load config failed", "err", err)
		os.Exit(1)
	}
	// 配置不合法就直接退出：带着坏配置常驻只会让告警静默丢失
	if err := cfg.Validate(); err != nil {
		logger.Error("invalid config", "err", err)
		os.Exit(1)
	}

	handler := forwarder.NewHandler(cfg, forwarder.NewSender(cfg), logger)

	srv := fserver.New(
		fserver.Address(cfg.ListenAddr),
		// 请求级超时略大于出站超时，保证飞书慢时是出站超时先触发并记成失败
		fserver.Timeout(cfg.FeishuTimeout+10*time.Second),
		fserver.MaxRequestBodySize(forwarder.MaxBodyBytes()),
		fserver.Middleware(
			fmiddleware.Recovery(),
			fmiddleware.RequestID(),
			fmiddleware.Logging(),
		),
	)
	srv.Route(http.MethodPost, "/webhook", handler.Webhook)
	srv.Route(http.MethodGet, "/healthz", handler.Health)
	srv.Route(http.MethodGet, "/readyz", handler.Ready)

	logger.Info("starting feishu-forwarder", "version", version, "commit", commit, "listen", cfg.ListenAddr)

	application := app.New(
		app.Name("feishu-forwarder"),
		app.Logger(logger),
		app.Server(srv),
		// 给在途请求留出退出时间，避免滚动更新时丢掉正在转发的那条告警
		app.StopTimeout(10*time.Second),
	)
	if code, err := application.Run(); err != nil {
		logger.Error("application exited with error", "err", err)
		os.Exit(code)
	}
}
