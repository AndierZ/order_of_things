// Command server serves the tournament to a browser.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"time"

	"order_of_things/internal/golden"
	"order_of_things/internal/session"
	"order_of_things/internal/web"
)

func main() {
	var (
		addr     = flag.String("addr", ":8080", "address to listen on")
		goldPath = flag.String("golden", "testdata/golden.json", "golden outcome store; empty keeps references in memory only")
		games    = flag.Int("games", session.DefaultGames, "games per session")
		interval = flag.Duration("interval", session.DefaultInterval, "pace of admission, one event per interval")
		idle     = flag.Duration("idle", 10*time.Minute, "stop sessions untouched for this long")
		maxAge   = flag.Duration("max-age", time.Hour, "stop sessions older than this")
		quiet    = flag.Bool("quiet", false, "suppress the platform's own logging")
	)
	flag.Parse()

	if *quiet {
		log.SetOutput(io.Discard)
	}

	store, err := golden.Open(*goldPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	registry := session.NewRegistry(store)
	registry.SetGames(*games)
	registry.SetInterval(*interval)
	defer registry.StopAll()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	// A viewer who closes the tab leaves a session running at one event per
	// second forever, so idle ones are reclaimed on a timer.
	go reclaim(ctx, registry, *idle, *maxAge)

	server := &http.Server{
		Addr:    *addr,
		Handler: web.NewServer(registry),
		// No write timeout: the event stream is a long-lived response.
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()

	fmt.Fprintf(os.Stderr, "the order of things: http://localhost%s  (%d games, %v per event)\n",
		*addr, *games, *interval)

	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func reclaim(ctx context.Context, registry *session.Registry, idle, maxAge time.Duration) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if stopped := registry.Evict(idle, maxAge); len(stopped) > 0 {
				log.Printf("reclaimed idle sessions: %v", stopped)
			}
		}
	}
}
