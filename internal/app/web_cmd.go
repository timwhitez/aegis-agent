package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"aegis-agent/internal/config"
	"aegis-agent/internal/webconsole"
)

const maxWebWorkerCount = 8

func webCommand(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	args = normalizeInterspersedFlags(args, []string{"config", "listen", "workers"}, nil)
	fs := flag.NewFlagSet("web", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		configPath = fs.String("config", "", "")
		listenAddr = fs.String("listen", "127.0.0.1:3940", "")
		workers    = fs.Int("workers", 2, "")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *workers < 0 {
		return fmt.Errorf("workers must be >= 0")
	}
	if *workers > maxWebWorkerCount {
		return fmt.Errorf("workers must be <= %d", maxWebWorkerCount)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	cfg, err := loadConfig(*configPath, cwd)
	if err != nil {
		return err
	}
	service, err := webconsole.New(cfg, webconsole.Options{
		WorkerCount: *workers,
		ConfigPath:  config.PersistPath(*configPath, cwd),
	})
	if err != nil {
		return err
	}
	defer service.Close()
	serveCtx, stop := context.WithCancel(ctx)
	defer stop()

	server := &http.Server{
		Addr:              *listenAddr,
		Handler:           webconsole.SecurityHeaders(service),
		ReadHeaderTimeout: 10 * time.Second,
		// IdleTimeout must be set explicitly: with IdleTimeout==0 net/http falls
		// back to ReadTimeout (also 0), so idle keep-alive connections would never
		// be reclaimed. No global ReadTimeout/WriteTimeout here on purpose: the
		// same server carries /ws long-lived connections and large workspace
		// downloads, which a whole-request deadline would cut off.
		IdleTimeout: 120 * time.Second,
	}

	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-serveCtx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	if webListenExposesNetwork(*listenAddr) {
		_, _ = fmt.Fprintln(stdout, "WARNING: web console is reachable from non-loopback clients. It can write config and .env API keys, delete sessions, manage skills, read/download/upload/rename workspace files, and create workspace folders or delete one or more workspace files or folders. Use only on trusted local networks.")
	}
	_, _ = fmt.Fprintf(stdout, "web console listening on http://%s\n", *listenAddr)
	err = server.ListenAndServe()
	stop()
	<-shutdownDone
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func webListenExposesNetwork(addr string) bool {
	host := strings.TrimSpace(addr)
	if parsedHost, _, err := net.SplitHostPort(addr); err == nil {
		host = parsedHost
	}
	host = strings.Trim(host, "[]")
	if host == "" || host == "*" || host == "0.0.0.0" || host == "::" {
		return true
	}
	if strings.EqualFold(host, "localhost") {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return !ip.IsLoopback()
	}
	return true
}
