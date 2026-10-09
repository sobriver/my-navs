package main

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

//go:embed web
var webFS embed.FS

type Config struct {
	SiteTitle     string
	NavPassword   string
	AdminPassword string
	SessionTTL    time.Duration
	Port          string
	DataDir       string
	TrustProxy    bool
}

func loadConfig() (*Config, error) {
	c := &Config{
		SiteTitle:     envOr("SITE_TITLE", "我的导航"),
		NavPassword:   os.Getenv("NAV_PASSWORD"),
		AdminPassword: os.Getenv("ADMIN_PASSWORD"),
		Port:          envOr("PORT", "8080"),
		DataDir:       envOr("DATA_DIR", "./data"),
		TrustProxy:    isTrue(os.Getenv("TRUST_PROXY")),
	}
	if c.NavPassword == "" {
		return nil, errors.New("必须设置环境变量 NAV_PASSWORD")
	}
	if c.AdminPassword == "" {
		c.AdminPassword = c.NavPassword
	}
	days := 30
	if v := os.Getenv("SESSION_DAYS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 3650 {
			return nil, fmt.Errorf("SESSION_DAYS 无效：%q", v)
		}
		days = n
	}
	c.SessionTTL = time.Duration(days) * 24 * time.Hour
	return c, nil
}

func main() {
	// scratch 镜像里没有 curl，Docker 健康检查直接调用自身
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(healthcheck())
	}

	cfg, err := loadConfig()
	if err != nil {
		log.Fatal(err)
	}

	iconDir := filepath.Join(cfg.DataDir, "icons")
	if err := os.MkdirAll(iconDir, 0o755); err != nil {
		log.Fatal(dataDirError(cfg.DataDir, err))
	}
	secret, err := loadSecret(filepath.Join(cfg.DataDir, ".secret"))
	if err != nil {
		log.Fatal(dataDirError(cfg.DataDir, err))
	}
	store, err := OpenStore(filepath.Join(cfg.DataDir, "navs.json"))
	if err != nil {
		log.Fatal(dataDirError(cfg.DataDir, err))
	}

	app, err := NewApp(cfg, store, NewAuth(cfg, secret), NewIcons(iconDir))
	if err != nil {
		log.Fatal(err)
	}

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           app.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	done := make(chan struct{})
	go func() {
		defer close(done)
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(sctx)
	}()

	log.Printf("my-navs 已启动，监听 :%s，数据目录 %s", cfg.Port, cfg.DataDir)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
	<-done
	log.Print("已退出")
}

func healthcheck() int {
	c := http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get("http://127.0.0.1:" + envOr("PORT", "8080") + "/healthz")
	if err != nil {
		return 1
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}

func dataDirError(dir string, err error) error {
	if errors.Is(err, fs.ErrPermission) {
		return fmt.Errorf("数据目录 %s 没有写权限：%v\n容器以 UID 65532 运行，请在宿主机执行：sudo chown -R 65532:65532 ./data", dir, err)
	}
	return err
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func isTrue(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}
