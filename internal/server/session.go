package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"

	"github.com/creack/pty"
	"golang.org/x/crypto/ssh"
)

type sessionHandler struct {
	srv      *Server
	channel  ssh.Channel
	requests <-chan *ssh.Request
	user     string
}

func (h *sessionHandler) handle(ctx context.Context) {
	defer func() {
		_ = h.channel.CloseWrite()
		_ = h.channel.Close()
	}()

	env := append([]string(nil), os.Environ()...)
	env = append(env, fmt.Sprintf("USER=%s", h.user))
	env = append(env, fmt.Sprintf("LOGNAME=%s", h.user))
	env = append(env, "HOME=/")
	env = append(env, fmt.Sprintf("SHELL=%s", h.srv.cfg.Shell))

	var (
		mu       sync.Mutex
		cmd      *exec.Cmd
		ptmx     *os.File
		wantPTY  bool
		cols     uint32
		rows     uint32
		finished sync.Once
	)

	start := func(command string) error {
		mu.Lock()
		defer mu.Unlock()
		if cmd != nil {
			err := errors.New("session already running")
			h.srv.logger.Warn("session start rejected", "user", h.user, "command", command, "err", err)
			return err
		}

		args := []string{}
		if command != "" {
			args = append(args, "-c", command)
		}

		c := exec.CommandContext(ctx, h.srv.cfg.Shell, args...)
		c.Env = append([]string(nil), env...)
		c.Dir = "/"

		started := false

		if wantPTY {
			var err error
			ws := &pty.Winsize{}
			if cols > 0 {
				ws.Cols = uint16(cols)
			}
			if rows > 0 {
				ws.Rows = uint16(rows)
			}
			if ws.Cols == 0 && ws.Rows == 0 {
				ptmx, err = pty.Start(c)
			} else {
				ptmx, err = pty.StartWithSize(c, ws)
			}
			if err != nil {
				h.srv.logger.Error("start pty shell failed", "user", h.user, "command", command, "err", err)
				return err
			}

			go func() {
				_, _ = io.Copy(h.channel, ptmx)
			}()
			go func() {
				_, _ = io.Copy(ptmx, h.channel)
			}()
			started = true
		} else {
			c.Stdout = h.channel
			c.Stderr = h.channel.Stderr()
			stdin, err := c.StdinPipe()
			if err != nil {
				h.srv.logger.Error("allocate stdin pipe failed", "user", h.user, "command", command, "err", err)
				return err
			}
			go func() {
				_, _ = io.Copy(stdin, h.channel)
				_ = stdin.Close()
			}()
		}

		if !started {
			if err := c.Start(); err != nil {
				if ptmx != nil {
					_ = ptmx.Close()
					ptmx = nil
				}
				h.srv.logger.Error("launch shell failed", "user", h.user, "command", command, "shell", h.srv.cfg.Shell, "err", err)
				return err
			}
		}

		cmd = c

		go func() {
			<-ctx.Done()
			mu.Lock()
			if cmd != nil && cmd.Process != nil {
				_ = cmd.Process.Signal(syscall.SIGTERM)
			}
			mu.Unlock()
		}()

		go func() {
			err := c.Wait()
			finished.Do(func() {
				h.sendExitStatus(err)
				if ptmx != nil {
					_ = ptmx.Close()
				}
			})
		}()

		return nil
	}

	for req := range h.requests {
		switch req.Type {
		case "pty-req":
			var payload struct {
				Term   string
				Cols   uint32
				Rows   uint32
				Width  uint32
				Height uint32
				Modes  string `ssh:"rest"`
			}
			if err := ssh.Unmarshal(req.Payload, &payload); err != nil {
				if req.WantReply {
					req.Reply(false, nil)
				}
				continue
			}
			wantPTY = true
			cols = payload.Cols
			rows = payload.Rows
			if payload.Term != "" {
				env = append(env, fmt.Sprintf("TERM=%s", payload.Term))
			}
			if req.WantReply {
				req.Reply(true, nil)
			}
		case "env":
			var payload struct {
				Key   string
				Value string
			}
			if err := ssh.Unmarshal(req.Payload, &payload); err != nil {
				if req.WantReply {
					req.Reply(false, nil)
				}
				continue
			}
			if payload.Key != "" {
				env = append(env, fmt.Sprintf("%s=%s", payload.Key, payload.Value))
				if req.WantReply {
					req.Reply(true, nil)
				}
			} else if req.WantReply {
				req.Reply(false, nil)
			}
		case "window-change":
			if ptmx != nil {
				var payload struct {
					Cols   uint32
					Rows   uint32
					Width  uint32
					Height uint32
				}
				if err := ssh.Unmarshal(req.Payload, &payload); err == nil {
					cols = payload.Cols
					rows = payload.Rows
					_ = pty.Setsize(ptmx, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
				}
			}
			if req.WantReply {
				req.Reply(true, nil)
			}
		case "shell":
			err := start("")
			if req.WantReply {
				req.Reply(err == nil, nil)
			}
			if err != nil {
				h.srv.logger.Error("shell request failed", "user", h.user, "err", err)
			}
		case "exec":
			var payload struct {
				Command string
			}
			if err := ssh.Unmarshal(req.Payload, &payload); err != nil {
				if req.WantReply {
					req.Reply(false, nil)
				}
				continue
			}
			err := start(payload.Command)
			if req.WantReply {
				req.Reply(err == nil, nil)
			}
			if err != nil {
				h.srv.logger.Error("exec request failed", "user", h.user, "command", payload.Command, "err", err)
			}
		case "signal":
			var payload struct {
				Signal string
			}
			if err := ssh.Unmarshal(req.Payload, &payload); err == nil {
				sig := sshSignalToOS(payload.Signal)
				if sig != nil {
					mu.Lock()
					if cmd != nil && cmd.Process != nil {
						_ = cmd.Process.Signal(sig)
					}
					mu.Unlock()
				}
			}
			if req.WantReply {
				req.Reply(true, nil)
			}
		default:
			if req.WantReply {
				req.Reply(false, nil)
			}
		}
	}
}

func (h *sessionHandler) sendExitStatus(err error) {
	status := uint32(0)
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			if code := exitErr.ExitCode(); code >= 0 {
				status = uint32(code)
			} else {
				status = 255
			}
		} else {
			status = 255
		}
	}

	_, _ = h.channel.SendRequest("exit-status", false, ssh.Marshal(struct {
		Status uint32
	}{Status: status}))
}

func sshSignalToOS(signal string) os.Signal {
	signal = strings.TrimPrefix(signal, "SIG")
	switch signal {
	case "INT":
		return syscall.SIGINT
	case "TERM":
		return syscall.SIGTERM
	case "KILL":
		return syscall.SIGKILL
	case "QUIT":
		return syscall.SIGQUIT
	case "HUP":
		return syscall.SIGHUP
	default:
		return nil
	}
}
