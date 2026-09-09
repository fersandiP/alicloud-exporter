package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to the YAML config file")
	flag.Parse()

	cfg, err := Load(*configPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	lister, err := newSDKLister(cfg.RegionID)
	if err != nil {
		log.Fatalf("alibaba cloud client: %v", err)
	}

	reg := prometheus.NewRegistry()
	col := NewCollector(cfg, lister, reg)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		col.pollOnce(ctx) // prime the cache
		t := time.NewTicker(cfg.PollInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				col.pollOnce(ctx)
			}
		}
	}()

	mux := http.NewServeMux()
	mux.Handle(cfg.MetricsPath, promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	idle := make(chan struct{})
	go func() {
		<-ctx.Done()
		shCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shCtx); err != nil {
			log.Printf("http shutdown: %v", err)
		}
		close(idle)
	}()

	log.Printf("alicloud-exporter %s listening on %s%s (poll every %s)", version, cfg.ListenAddr, cfg.MetricsPath, cfg.PollInterval)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("http server: %v", err)
	}
	<-idle
}
