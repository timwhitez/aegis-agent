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

	"go-cli-agent/internal/config"
	"go-cli-agent/internal/webconsole"
)

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
	cwd, _ := os.Getwd()
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
		Handler:           service,
		ReadHeaderTimeout: 10 * time.Second,
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
		_, _ = fmt.Fprintln(stdout, "WARNING: experimental web is reachable from non-loopback clients. It can write config and .env API keys, delete sessions, manage skills, and read workspace files. Use only on trusted local networks.")
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
