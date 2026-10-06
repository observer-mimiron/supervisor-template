// Package main 是 M0 的进程入口。
//
// 本文件负责读取配置、完成装配和提供健康检查，不推进 Agent 业务流程。
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/joho/godotenv"

	"github.com/observer-mimiron/supervisor-template/internal/composition"
	"github.com/observer-mimiron/supervisor-template/internal/config"
	httpapi "github.com/observer-mimiron/supervisor-template/internal/interfaces/http"
)

// shutdownGrace 是收到终止信号后等待在途响应收尾的窗口。
const shutdownGrace = 30 * time.Second

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
		Addr: cfg.Server.ListenAddr,
		Handler: httpapi.NewRouter(app.Run, app.Health, app.Authenticator, httpapi.Options{
			RequestBodyLimit: cfg.Server.RequestBodyLimit,
			SSEHeartbeat:     cfg.Server.SSEHeartbeat,
			WriteTimeout:     cfg.Server.WriteTimeout,
		}),
		ReadTimeout: cfg.Server.ReadTimeout,
		IdleTimeout: cfg.Server.IdleTimeout,
		// WriteTimeout 故意为 0：它是从读完请求头算起的绝对上限，会截断等待审批或
		// 慢 Tool 的长 Run。写空闲上限改由每个请求用 ResponseController 自行刷新。
	}
	if app.Logger != nil {
		app.Logger.Info("服务启动", "listen_addr", cfg.Server.ListenAddr, "model_provider", cfg.Model.Provider, "fake_mode", *fakeMode)
	} else {
		log.Printf("服务启动: %s", cfg.Server.ListenAddr)
	}
	serveAndShutdown(server, app)
}

// serveAndShutdown 在收到 SIGINT/SIGTERM 时停止接收新请求，给在途响应一个收尾窗口。
//
// 没有这一步时进程被编排系统重启会直接消失：defer 不会执行，close 不会跑，
// in-flight Run 只能等到租约过期后进入 waiting_reconciliation。
func serveAndShutdown(server *http.Server, app *composition.App) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- server.ListenAndServe()
	}()

	select {
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			if app.Logger != nil {
				app.Logger.Error("服务退出", "error", err)
				return
			}
			log.Fatal(err)
		}
	case <-ctx.Done():
		stop()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			if app.Logger != nil {
				app.Logger.Error("服务关闭超时", "error", err)
			} else {
				log.Printf("服务关闭超时: %v", err)
			}
		}
		if app.Logger != nil {
			app.Logger.Info("服务已停止")
		}
	}
}

func forceFakeModel(cfg *config.Config) {
	cfg.Model.Provider = "fake"
	cfg.Model.Name = "fake-model"
}
