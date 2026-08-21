package main

import (
	"log"
	"os"
	"path/filepath"
	"syscall"

	"git.ipao.vip/rogee/mohomo-docker/internal/bootstrap"
)

const (
	defaultRoot         = "/opt/clash"
	defaultCoreSource   = "/usr/local/lib/ssclash/clash"
	defaultConfigSource = "/usr/local/share/ssclash/config.yaml"
	ssclashBinary       = "/usr/local/bin/ssclash"
)

func main() {
	log.SetFlags(log.Ldate | log.Ltime | log.LUTC)
	root := envOrDefault("SSCLASH_ROOT", defaultRoot)
	log.Printf("bootstrap: preparing persistent runtime root=%s", root)

	result, err := bootstrap.Prepare(bootstrap.Config{
		Root:         root,
		CoreSource:   defaultCoreSource,
		ConfigSource: defaultConfigSource,
	})
	if err != nil {
		log.Fatalf("bootstrap: runtime preparation failed: %v", err)
	}
	log.Printf(
		"bootstrap: ready root=%s core_initialized=%t config_initialized=%t server_mode_changed=%t",
		root,
		result.CoreInitialized,
		result.ConfigInitialized,
		result.ServerModeChanged,
	)

	arguments := os.Args[1:]
	if len(arguments) == 0 {
		arguments = []string{"serve"}
	}
	argv := append([]string{filepath.Base(ssclashBinary)}, arguments...)
	log.Printf("bootstrap: exec path=%s command=%s", ssclashBinary, arguments[0])
	if err := syscall.Exec(ssclashBinary, argv, os.Environ()); err != nil {
		log.Fatalf("bootstrap: exec failed: %v", err)
	}
}

func envOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
