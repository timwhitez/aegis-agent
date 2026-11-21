package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"aegis-agent/internal/fileutil"
)

// Only the fixed container loopback listener can access this attempt's Unix socket.
func bridge(socket string) (net.Listener, error) {
	ln, err := net.Listen("tcp4", "127.0.0.1:19487")
	if err != nil {
		return nil, err
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				upstream, err := net.DialTimeout("unix", socket, 5*time.Second)
				if err != nil {
					return
				}
				defer upstream.Close()
				go func() { _, _ = io.Copy(upstream, c); _ = upstream.(*net.UnixConn).CloseWrite() }()
				_, _ = io.Copy(c, upstream)
			}()
		}
	}()
	return ln, nil
}

func run(args []string) int {
	flags := flag.NewFlagSet("benchevidence", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	sessions := flags.String("sessions", "", "private session directory")
	output := flags.String("output", "", "destination of safe JSON")
	socket := flags.String("provider-socket", "", "mounted host credential relay")
	check := flags.Bool("check", false, "validate projected evidence on stdin")
	forwardStdin := flags.Bool("forward-stdin", false, "pass standard input to the benchmark child")
	inspect := flags.Bool("collect", false, "project existing sessions after interruption")
	if flags.Parse(args) != nil {
		return 2
	}
	if *check {
		r, err := checked(os.Stdin)
		if err != nil {
			return 2
		}
		if json.NewEncoder(os.Stdout).Encode(r) != nil {
			return 2
		}
		return 0
	}
	if *sessions == "" || *output == "" || (*inspect && flags.NArg() != 0) || (!*inspect && flags.NArg() == 0) {
		return 2
	}
	var code *int
	if !*inspect {
		if *socket != "" {
			ln, err := bridge(*socket)
			if err != nil {
				return 125
			}
			defer ln.Close()
		}
		child := exec.Command(flags.Arg(0), flags.Args()[1:]...)
		if *forwardStdin {
			child.Stdin = os.Stdin
		}
		// nil stdout/stderr in os/exec connect to the null device. The canonical
		// session supplies evidence even when rendering is interrupted.
		child.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		signals := make(chan os.Signal, 1)
		signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
		defer signal.Stop(signals)
		rc := 127
		if child.Start() == nil {
			done := make(chan error, 1)
			go func() { done <- child.Wait() }()
			select {
			case <-done:
				rc = child.ProcessState.ExitCode()
				if rc < 0 {
					rc = 128 + int(child.ProcessState.Sys().(syscall.WaitStatus).Signal())
				}
			case sig := <-signals:
				_ = syscall.Kill(-child.Process.Pid, syscall.SIGTERM)
				select {
				case <-done:
				case <-time.After(3 * time.Second):
					_ = syscall.Kill(-child.Process.Pid, syscall.SIGKILL)
					<-done
				}
				rc = 128 + int(sig.(syscall.Signal))
			}
		}
		code = &rc
	}
	data, err := encode(collect(*sessions, code))
	if err != nil || fileutil.AtomicWriteFileNoSymlink(*output, data, 0600) != nil {
		fmt.Fprintln(os.Stderr, "unable to write benchmark evidence")
		return 125
	}
	if code != nil {
		return *code
	}
	return 0
}

func main() { os.Exit(run(os.Args[1:])) }
