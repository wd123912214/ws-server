package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

var (
	version = "dev" // 构建时用 -ldflags "-X main.version=x.y.z" 注入
	logger  = slog.New(slog.NewTextHandler(os.Stdout, nil))
)

func main() {
	configPath := flag.String("config", "config.json", "配置文件路径")
	showVersion := flag.Bool("version", false, "显示版本")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return
	}

	cfg, created, err := loadConfig(*configPath)
	if err != nil {
		logger.Error("读取配置失败", "path", *configPath, "err", err)
		os.Exit(1)
	}
	if created {
		logger.Info("已生成默认配置，请记下面板口令", "path", *configPath, "panel_password", cfg.PanelPassword)
	}
	guard, err := newGuard(cfg)
	if err != nil {
		logger.Error("初始化黑名单失败", "err", err)
		os.Exit(1)
	}

	s := newServer(cfg, guard)
	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           s.mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		logger.Info("启动", "version", version, "listen", cfg.ListenAddr, "token", cfg.Token != "", "trust_proxy", cfg.TrustProxy)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("监听失败", "err", err)
			os.Exit(1)
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	logger.Info("正在停止")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}
