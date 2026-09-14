package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/de-wim/webd"
)

func main() {
	configPath := flag.String("config", "webd.json", "path to the config file")
	podmanBin := flag.String("podman", "", "path to the podman binary (default \"podman\")")
	flag.Parse()

	logger := log.New(os.Stderr, "webd: ", log.LstdFlags)
	data, err := os.ReadFile(*configPath)
	if err != nil {
		logger.Fatalf("config: %v", err)
	}
	cfg, warnings, err := webd.ParseConfig(data)
	for _, w := range warnings {
		logger.Printf("warning: %s", w)
	}
	if err != nil {
		logger.Fatalf("config: %v", err)
	}

	srv, err := webd.New(cfg, webd.Options{Runtime: &webd.Podman{Bin: *podmanBin}, Log: logger})
	if err != nil {
		logger.Fatal(err)
	}
	if err := srv.Start(); err != nil {
		logger.Fatal(err)
	}

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigs
	logger.Printf("received %s, shutting down", sig)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		logger.Printf("shutdown: %v", err)
	}
}
