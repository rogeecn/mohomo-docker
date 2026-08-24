package main

import (
	"context"
	"errors"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"git.ipao.vip/rogee/mohomo-docker/internal/bootstrap"
)

const (
	defaultRoot         = "/opt/clash"
	defaultSSClashTemp  = "/tmp/ssclash"
	defaultCoreSource   = "/usr/local/lib/ssclash/clash"
	defaultConfigSource = "/usr/local/share/ssclash/config.yaml"
	ssclashBinary       = "/usr/local/bin/ssclash"
	defaultRuntimeDir   = "/dev/shm/mohomo"
)

func main() {
	log.SetFlags(log.Ldate | log.Ltime | log.LUTC)
	root := envOrDefault("SSCLASH_ROOT", defaultRoot)
	log.Printf("bootstrap: preparing persistent runtime root=%s", root)

	result, err := bootstrap.Prepare(bootstrap.Config{
		Root:         root,
		SSClashTemp:  envOrDefault("SSCLASH_TMP", defaultSSClashTemp),
		CoreSource:   defaultCoreSource,
		ConfigSource: defaultConfigSource,
	})
	if err != nil {
		log.Fatalf("bootstrap: runtime preparation failed: %v", err)
	}
	adminPasswordInitialized, err := bootstrap.EnsureAdminPassword(root, ssclashBinary, os.Getenv("SSCLASH_PASSWORD"))
	if err != nil {
		log.Fatalf("bootstrap: admin authentication setup failed: %v", err)
	}
	log.Printf(
		"bootstrap: ready root=%s core_initialized=%t config_initialized=%t server_settings_changed=%t admin_password_initialized=%t",
		root,
		result.CoreInitialized,
		result.ConfigInitialized,
		result.ServerSettingsChanged,
		adminPasswordInitialized,
	)

	subscriptionURL := os.Getenv("SUBSCRIPTION_URL")
	if subscriptionURL == "" {
		log.Fatal("bootstrap: SUBSCRIPTION_URL is required")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err = bootstrap.Run(ctx, bootstrap.RuntimeConfig{
		CoreBinary:      defaultCoreSource,
		SSClashBinary:   ssclashBinary,
		ConfigSource:    defaultConfigSource,
		RuntimeDir:      defaultRuntimeDir,
		SubscriptionURL: subscriptionURL,
		UpdateInterval:  time.Hour,
	})
	if err != nil && !errors.Is(err, context.Canceled) {
		log.Fatalf("bootstrap: service failed: %v", err)
	}
}

func envOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
