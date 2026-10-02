// Package main 是 M0 的进程入口。
//
// 本文件负责读取配置、完成装配和提供健康检查，不推进 Agent 业务流程。
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"github.com/joho/godotenv"

	"github.com/observer-mimiron/supervisor-template/internal/composition"
	"github.com/observer-mimiron/supervisor-template/internal/config"
	httpapi "github.com/observer-mimiron/supervisor-template/internal/interfaces/http"
)

func main() {
	configPath := flag.String("f", "config.example.toml", "配置文件路径")
	fakeMode := flag.Bool("fake", false, "强制使用本地 fake Supervisor，忽略 MODEL_PROVIDER/deepseek 环境覆盖")
	flag.Parse()
	if err := godotenv.Load(filepath.Join(filepath.Dir(*configPath), ".env")); err != nil && !os.IsNotExist(err) {
		log.Fatalf("读取 .env 失败: %v", err)
	}
	loadConfig := config.Load
	if *fakeMode {
		loadConfig = config.LoadWithFake
	}
	cfg, err := loadConfig(*configPath)
	if err != nil {
		log.Fatalf("启动配置无效: %v", err)
	}
	if *fakeMode {
		forceFakeModel(&cfg)
	}
	app, err := composition.New(cfg)
	if err != nil {
		log.Fatalf("启动装配失败: %v", err)
	}
	defer func() {
		if app.Close != nil {
			_ = app.Close(context.Background())
		}
	}()
	server := &http.Server{
		Addr:         cfg.Server.ListenAddr,
		Handler:      httpapi.NewRouter(app.Run, app.Health, app.Authenticator),
		ReadTimeout:  cfg.Server.ReadTimeout,
		WriteTimeout: cfg.Server.WriteTimeout,
		IdleTimeout:  cfg.Server.IdleTimeout,
	}
	if app.Logger != nil {
		app.Logger.Info("服务启动", "listen_addr", cfg.Server.ListenAddr, "model_provider", cfg.Model.Provider, "fake_mode", *fakeMode)
	} else {
		log.Printf("服务启动: %s", cfg.Server.ListenAddr)
	}
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		if app.Logger != nil {
			app.Logger.Error("服务退出", "error", err)
		} else {
			log.Fatal(err)
		}
	}
}

func forceFakeModel(cfg *config.Config) {
	cfg.Model.Provider = "fake"
	cfg.Model.Name = "fake-model"
}
