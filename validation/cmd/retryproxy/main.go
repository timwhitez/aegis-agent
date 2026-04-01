package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

type requestLog struct {
	Time          string `json:"time"`
	Method        string `json:"method"`
	Path          string `json:"path"`
	Query         string `json:"query,omitempty"`
	MatchedMarker bool   `json:"matched_marker"`
	MatchedDelay  bool   `json:"matched_delay,omitempty"`
	Injected      bool   `json:"injected"`
	DelayInjected bool   `json:"delay_injected,omitempty"`
	Canceled      bool   `json:"canceled,omitempty"`
	ForwardURL    string `json:"forward_url,omitempty"`
	StatusCode    int    `json:"status_code,omitempty"`
	Error         string `json:"error,omitempty"`
	RequestBytes  int    `json:"request_bytes"`
	ResponseBytes int    `json:"response_bytes,omitempty"`
}

type logWriter struct {
	mu   sync.Mutex
	file *os.File
}

func newLogWriter(path string) (*logWriter, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	return &logWriter{file: file}, nil
}

func (w *logWriter) Close() error {
	if w == nil || w.file == nil {
		return nil
	}
	return w.file.Close()
}

func (w *logWriter) Write(entry requestLog) {
	if w == nil || w.file == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	enc := json.NewEncoder(w.file)
	_ = enc.Encode(entry)
}

func main() {
	var (
		listenAddr  = flag.String("listen", "127.0.0.1:0", "")
		upstream    = flag.String("upstream", "", "")
		match       = flag.String("match-substring", "", "")
		delayMatch  = flag.String("delay-match-substring", "", "")
		delayMS     = flag.Int("delay-ms", 0, "")
		readyFile   = flag.String("ready-file", "", "")
		requestLogf = flag.String("request-log", "", "")
	)
	flag.Parse()

	if strings.TrimSpace(*upstream) == "" {
		log.Fatal("upstream is required")
	}
	upstreamURL, err := url.Parse(*upstream)
	if err != nil {
		log.Fatalf("parse upstream: %v", err)
	}
	if upstreamURL.Scheme == "" || upstreamURL.Host == "" {
		log.Fatal("upstream must be an absolute URL")
	}

	reqLogger, err := newLogWriter(*requestLogf)
	if err != nil {
		log.Fatalf("open request log: %v", err)
	}
	defer reqLogger.Close()

	var injected atomic.Bool
	var delayed atomic.Bool
	client := &http.Client{Timeout: 240 * time.Second}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
			return
		}
		_ = r.Body.Close()

		matched := strings.TrimSpace(*match) != "" && strings.Contains(string(body), *match)
		matchedDelay := strings.TrimSpace(*delayMatch) != "" && strings.Contains(string(body), *delayMatch)
		if matched && injected.CompareAndSwap(false, true) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			payload := []byte(`{"error":"retryproxy injected upstream_unavailable"}` + "\n")
			_, _ = w.Write(payload)
			reqLogger.Write(requestLog{
				Time:          time.Now().UTC().Format(time.RFC3339Nano),
				Method:        r.Method,
				Path:          r.URL.Path,
				Query:         r.URL.RawQuery,
				MatchedMarker: true,
				Injected:      true,
				StatusCode:    http.StatusServiceUnavailable,
				RequestBytes:  len(body),
				ResponseBytes: len(payload),
			})
			return
		}
		delayApplied := false
		if matchedDelay && *delayMS > 0 && delayed.CompareAndSwap(false, true) {
			delayApplied = true
			timer := time.NewTimer(time.Duration(*delayMS) * time.Millisecond)
			select {
			case <-timer.C:
			case <-r.Context().Done():
				if !timer.Stop() {
					<-timer.C
				}
				reqLogger.Write(requestLog{
					Time:          time.Now().UTC().Format(time.RFC3339Nano),
					Method:        r.Method,
					Path:          r.URL.Path,
					Query:         r.URL.RawQuery,
					MatchedMarker: matched,
					MatchedDelay:  true,
					Injected:      false,
					DelayInjected: true,
					Canceled:      true,
					Error:         r.Context().Err().Error(),
					RequestBytes:  len(body),
				})
				return
			}
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		}

		forwardURL := *upstreamURL
		forwardURL.Path = joinURLPath(upstreamURL.Path, r.URL.Path)
		forwardURL.RawQuery = r.URL.RawQuery

		req, err := http.NewRequestWithContext(r.Context(), r.Method, forwardURL.String(), bytes.NewReader(body))
		if err != nil {
			http.Error(w, "build upstream request: "+err.Error(), http.StatusInternalServerError)
			return
		}
		req.Header = r.Header.Clone()
		req.Host = upstreamURL.Host

		resp, err := client.Do(req)
		if err != nil {
			http.Error(w, "forward upstream: "+err.Error(), http.StatusBadGateway)
			reqLogger.Write(requestLog{
				Time:          time.Now().UTC().Format(time.RFC3339Nano),
				Method:        r.Method,
				Path:          r.URL.Path,
				Query:         r.URL.RawQuery,
				MatchedMarker: matched,
				MatchedDelay:  matchedDelay,
				Injected:      false,
				DelayInjected: delayApplied,
				ForwardURL:    forwardURL.String(),
				Error:         err.Error(),
				RequestBytes:  len(body),
			})
			return
		}
		defer resp.Body.Close()

		copyHeaders(w.Header(), resp.Header)
		w.WriteHeader(resp.StatusCode)
		written, copyErr := io.Copy(w, resp.Body)
		if copyErr != nil {
			reqLogger.Write(requestLog{
				Time:          time.Now().UTC().Format(time.RFC3339Nano),
				Method:        r.Method,
				Path:          r.URL.Path,
				Query:         r.URL.RawQuery,
				MatchedMarker: matched,
				MatchedDelay:  matchedDelay,
				Injected:      false,
				DelayInjected: delayApplied,
				ForwardURL:    forwardURL.String(),
				StatusCode:    resp.StatusCode,
				Error:         copyErr.Error(),
				RequestBytes:  len(body),
			})
			return
		}
		reqLogger.Write(requestLog{
			Time:          time.Now().UTC().Format(time.RFC3339Nano),
			Method:        r.Method,
			Path:          r.URL.Path,
			Query:         r.URL.RawQuery,
			MatchedMarker: matched,
			MatchedDelay:  matchedDelay,
			Injected:      false,
			DelayInjected: delayApplied,
			ForwardURL:    forwardURL.String(),
			StatusCode:    resp.StatusCode,
			RequestBytes:  len(body),
			ResponseBytes: int(written),
		})
	})

	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	listener, err := net.Listen("tcp", *listenAddr)
	if err != nil {
		log.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	if strings.TrimSpace(*readyFile) != "" {
		if err := os.WriteFile(*readyFile, []byte("http://"+listener.Addr().String()+"\n"), 0o600); err != nil {
			log.Fatalf("write ready file: %v", err)
		}
	}

	signalCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-signalCtx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	log.Printf("retryproxy listening on http://%s forwarding to %s", listener.Addr().String(), upstreamURL.String())
	if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
		log.Fatalf("serve: %v", err)
	}
}

func joinURLPath(basePath, reqPath string) string {
	base := strings.TrimRight(basePath, "/")
	if base == "" {
		if strings.HasPrefix(reqPath, "/") {
			return reqPath
		}
		return "/" + reqPath
	}
	if reqPath == "" || reqPath == "/" {
		return base
	}
	if strings.HasPrefix(reqPath, "/") {
		return base + reqPath
	}
	return base + "/" + reqPath
}

func copyHeaders(dst, src http.Header) {
	for key, values := range src {
		if strings.EqualFold(key, "Content-Length") {
			continue
		}
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}
