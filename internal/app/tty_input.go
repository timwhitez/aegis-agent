package app

import (
	"context"
	"io"
	"os"
	"sync"

	"golang.org/x/term"

	"aegis-agent/internal/runtime"
	"aegis-agent/internal/session"
)

// cliTTYInput is the single reader for a run command's terminal. Outside an
// interactive prompt it interprets Esc as an interrupt. A Plan Mode prompt
// takes an explicit lease, so every byte read while that lease is active is
// delivered to the prompt instead of being discarded by the Esc watcher.
type cliTTYInput struct {
	ctx        context.Context
	cancel     context.CancelFunc
	stdin      *os.File
	reader     *cancelableTTYReader
	rawBytes   chan byte
	acquire    chan cliTTYAcquireRequest
	release    chan cliTTYReleaseRequest
	done       chan struct{}
	readerDone chan struct{}
}

type cliTTYPromptLease struct {
	owner *cliTTYInput
	input chan byte
	once  sync.Once
}

type cliTTYAcquireRequest struct {
	lease *cliTTYPromptLease
	ready chan struct{}
}

type cliTTYReleaseRequest struct {
	lease *cliTTYPromptLease
	ready chan struct{}
}

func startCLITTYInput(parent context.Context, stdin *os.File, onInterrupt func()) (*cliTTYInput, error) {
	reader, err := newCancelableTTYReader(stdin)
	if err != nil {
		return nil, err
	}
	fd := int(stdin.Fd())
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		reader.Close()
		return nil, err
	}
	ctx, cancel := context.WithCancel(parent)
	input := &cliTTYInput{
		ctx:        ctx,
		cancel:     cancel,
		stdin:      stdin,
		reader:     reader,
		rawBytes:   make(chan byte),
		acquire:    make(chan cliTTYAcquireRequest),
		release:    make(chan cliTTYReleaseRequest),
		done:       make(chan struct{}),
		readerDone: make(chan struct{}),
	}
	go input.read()
	go input.dispatch(oldState, onInterrupt)
	return input, nil
}

func (c *cliTTYInput) read() {
	buffer := make([]byte, 64)
	defer close(c.readerDone)
	defer close(c.rawBytes)
	defer c.reader.Close()
	for {
		n, err := c.reader.Read(buffer)
		for _, value := range buffer[:n] {
			select {
			case c.rawBytes <- value:
			case <-c.done:
				return
			}
		}
		if err != nil || n == 0 {
			return
		}
	}
}

func (c *cliTTYInput) dispatch(oldState *term.State, onInterrupt func()) {
	defer close(c.done)
	defer term.Restore(int(c.stdin.Fd()), oldState) //nolint:errcheck // best-effort terminal recovery on every exit path

	var active *cliTTYPromptLease
	for {
		if active == nil {
			select {
			case <-c.ctx.Done():
				return
			case request := <-c.acquire:
				active = request.lease
				close(request.ready)
			case value, ok := <-c.rawBytes:
				if !ok {
					return
				}
				if value == 27 {
					onInterrupt()
					return
				}
			}
			continue
		}

		select {
		case <-c.ctx.Done():
			return
		case request := <-c.release:
			if request.lease == active {
				active = nil
			}
			close(request.ready)
		case value, ok := <-c.rawBytes:
			if !ok {
				return
			}
			select {
			case active.input <- value:
			case <-c.ctx.Done():
				return
			case request := <-c.release:
				if request.lease == active {
					active = nil
				}
				close(request.ready)
				if value == 27 {
					onInterrupt()
					return
				}
			}
		}
	}
}

func (c *cliTTYInput) close() {
	c.cancel()
	c.reader.Cancel()
	<-c.done
	<-c.readerDone
}

func (c *cliTTYInput) acquirePrompt(ctx context.Context) (*cliTTYPromptLease, error) {
	lease := &cliTTYPromptLease{owner: c, input: make(chan byte)}
	ready := make(chan struct{})
	request := cliTTYAcquireRequest{lease: lease, ready: ready}
	select {
	case c.acquire <- request:
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.done:
		return nil, io.EOF
	}
	select {
	case <-ready:
	case <-c.done:
		return nil, io.EOF
	}
	if err := ctx.Err(); err != nil {
		lease.release()
		return nil, err
	}
	return lease, nil
}

func (l *cliTTYPromptLease) release() {
	l.once.Do(func() {
		ready := make(chan struct{})
		request := cliTTYReleaseRequest{lease: l, ready: ready}
		select {
		case l.owner.release <- request:
		case <-l.owner.done:
			return
		}
		select {
		case <-ready:
		case <-l.owner.done:
		}
	})
}

func (l *cliTTYPromptLease) readLine(ctx context.Context) (string, error) {
	line := make([]byte, 0, 64)
	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-l.owner.done:
			return "", io.EOF
		case value := <-l.input:
			switch value {
			case '\r', '\n':
				return string(line), nil
			default:
				line = append(line, value)
			}
		}
	}
}

func (c *cliTTYInput) planInputHandler(stderr io.Writer) runtime.PlanInputHandler {
	return func(ctx context.Context, request session.PlanModeInputRequest) ([]session.PlanModeInputAnswer, error) {
		lease, err := c.acquirePrompt(ctx)
		if err != nil {
			return nil, err
		}
		defer lease.release()
		return collectCLIPlanInput(ctx, request, stderr, lease.readLine)
	}
}
