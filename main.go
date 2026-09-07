package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func main() {
	// 加载配置：config.yaml（可选）+ 环境变量 PORT/HEADLESS 覆盖
	cfg, err := loadConfig("config.yaml")
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}

	// 初始化浏览器管理器。默认有头模式，便于首次手动登录；config/HEADLESS 可切换无头。
	browserManager, err := NewBrowserManager(
		WithHeadless(cfg.Headless),
		WithUserDataDir(cfg.UserDataDir),
	)
	if err != nil {
		log.Fatalf("初始化浏览器失败: %v", err)
	}
	defer browserManager.Close()

	// 有头模式：主动打开 1688 首页，方便首次手动登录。
	// rod 启动浏览器带 --no-startup-window，不开页面就不会有可见窗口。
	if !cfg.Headless {
		if _, err := browserManager.NewPage("https://www.1688.com"); err != nil {
			log.Printf("打开 1688 首页失败（不影响服务）: %v", err)
		} else {
			log.Printf("已打开 1688 首页，请在浏览器窗口中完成登录（登录态保存在 %s）", cfg.UserDataDir)
		}
	}

	// 初始化搜索与详情服务
	searchService := NewSearchService(browserManager, cfg)
	detailService := NewDetailService(browserManager, cfg)

	// 初始化 MCP 服务器
	server := NewMCPServer(searchService, detailService)

	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", server.HandleHTTP)

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Ctrl+C / SIGTERM 后优雅退出：先停 HTTP，再由 defer 关闭浏览器
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		<-ctx.Done()
		log.Println("收到退出信号，正在关闭...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	fmt.Printf("1688 MCP Server 启动在端口 %s（headless=%v，登录态目录 %s）\n", cfg.Port, cfg.Headless, cfg.UserDataDir)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("HTTP 服务异常退出: %v", err)
	}
}

func parseBool(s string) bool {
	return s == "1" || strings.EqualFold(s, "true")
}
