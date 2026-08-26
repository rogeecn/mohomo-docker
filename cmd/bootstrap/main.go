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
	defaultCoreSource   = "/usr/local/bin/mihomo"
	defaultConfigSource = "/usr/local/share/mihomo/config.yaml"
	defaultSecretPath   = "/run/secrets/subscription"
	defaultDataDir      = "/data"
)

func main() {
	log.SetFlags(log.Ldate | log.Ltime | log.LUTC)
	if len(os.Args) == 2 && os.Args[1] == "candidate" {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if err := bootstrap.PublishCandidate(ctx, bootstrap.CandidateConfig{
			SecretPath:   defaultSecretPath,
			DataDir:      defaultDataDir,
			TemplatePath: defaultConfigSource,
			MihomoBinary: defaultCoreSource,
		}); err != nil {
			log.Fatalf("bootstrap: candidate update failed: %v", err)
		}
		log.Print("bootstrap: candidate configuration published")
		return
	}
	if len(os.Args) != 1 {
		log.Fatal("bootstrap: usage: bootstrap [candidate]")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	trigger := make(chan os.Signal, 1)
	signal.Notify(trigger, syscall.SIGHUP)
	defer signal.Stop(trigger)
	err := bootstrap.Run(ctx, bootstrap.LifecycleConfig{
		Candidate: bootstrap.CandidateConfig{
			SecretPath:   defaultSecretPath,
			DataDir:      defaultDataDir,
			TemplatePath: defaultConfigSource,
			MihomoBinary: defaultCoreSource,
		},
		UpdateInterval: time.Hour,
		Trigger:        trigger,
	})
	if err != nil && !errors.Is(err, context.Canceled) {
		log.Fatalf("bootstrap: service failed: %v", err)
	}
}
