package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Provokke/signpost/internal/shortener"
)

func main() {
	addr := flag.String("addr", ":8080", "address to listen on")
	data := flag.String("data", "signpost.log", "path of the link store")
	base := flag.String("base-url", "http://localhost:8080", "public base URL used in short links")
	compactEvery := flag.Duration("compact", time.Hour, "how often to compact the store (0 to disable)")
	flag.Parse()
	logger := log.New(os.Stderr, "signpost: ", log.LstdFlags)

	store, err := shortener.OpenStore(*data, nil)
	if err != nil {
		logger.Fatal(err)
	}
	defer store.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if *compactEvery > 0 {
		go func() {
			ticker := time.NewTicker(*compactEvery)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					if dropped, err := store.Compact(); err != nil {
						logger.Printf("compact failed: %v", err)
					} else if dropped > 0 {
						logger.Printf("compacted store, dropped %d expired links", dropped)
					}
				}
			}
		}()
	}

	server := &http.Server{
		Addr:              *addr,
		Handler:           shortener.NewServer(store, *base),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		server.Shutdown(shutdown)
	}()

	logger.Printf("listening on %s, storing links in %s", *addr, *data)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Fatal(err)
	}
}
