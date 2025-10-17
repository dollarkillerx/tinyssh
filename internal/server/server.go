package server

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/subtle"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/dollarkillerx/tinyssh/internal/config"
)

// Server represents a running tiny SSH server instance.
type Server struct {
	cfg     *config.Config
	creds   map[string]string
	hostKey ssh.Signer
	logger  *slog.Logger
}

// New creates a new Server instance based on the provided configuration.
func New(cfg *config.Config, logger *slog.Logger) (*Server, error) {
	if cfg == nil {
		return nil, errors.New("config is required")
	}

	if logger == nil {
		logger = slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	}

	hostKey, err := loadOrCreateHostKey(cfg.HostKeyPath)
	if err != nil {
		return nil, err
	}

	return &Server{
		cfg:     cfg,
		creds:   cfg.Credentials(),
		hostKey: hostKey,
		logger:  logger,
	}, nil
}

// Run starts the SSH server and blocks until the context is cancelled or an error occurs.
func (s *Server) Run(ctx context.Context) error {
	sshCfg := &ssh.ServerConfig{
		PasswordCallback: s.validateUser,
		ServerVersion:    "SSH-2.0-tinyssh",
	}
	sshCfg.AddHostKey(s.hostKey)

	listener, err := net.Listen("tcp", s.cfg.ListenAddress)
	if err != nil {
		return fmt.Errorf("listen %s: %w", s.cfg.ListenAddress, err)
	}
	s.logger.Info("listening", "address", listener.Addr().String())

	defer func() {
		if cerr := listener.Close(); cerr != nil {
			s.logger.Warn("listener close", "err", cerr)
		}
	}()

	var wg sync.WaitGroup

	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()

	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			if ne, ok := err.(net.Error); ok && ne.Temporary() {
				s.logger.Warn("temporary accept error", "err", err)
				time.Sleep(100 * time.Millisecond)
				continue
			}
			return fmt.Errorf("accept connection: %w", err)
		}

		wg.Add(1)
		go func(netConn net.Conn) {
			defer wg.Done()
			if err := s.handleConnection(ctx, netConn, sshCfg); err != nil {
				s.logger.Warn("connection ended", "remote", netConn.RemoteAddr().String(), "err", err)
			}
		}(conn)
	}

	wg.Wait()
	return nil
}

func (s *Server) validateUser(conn ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
	expected, ok := s.creds[conn.User()]
	if !ok {
		return nil, fmt.Errorf("unknown user %s", conn.User())
	}
	if subtle.ConstantTimeCompare([]byte(expected), password) != 1 {
		return nil, fmt.Errorf("invalid credentials for %s", conn.User())
	}
	return nil, nil
}

func (s *Server) handleConnection(ctx context.Context, netConn net.Conn, sshCfg *ssh.ServerConfig) error {
	defer func() {
		_ = netConn.Close()
	}()

	sshConn, channels, requests, err := ssh.NewServerConn(netConn, sshCfg)
	if err != nil {
		return fmt.Errorf("handshake failed: %w", err)
	}
	s.logger.Info("client connected", "user", sshConn.User(), "remote", sshConn.RemoteAddr().String())

	go ssh.DiscardRequests(requests)

	for newChannel := range channels {
		if newChannel.ChannelType() != "session" {
			newChannel.Reject(ssh.UnknownChannelType, "unknown channel type")
			continue
		}

		channel, requests, err := newChannel.Accept()
		if err != nil {
			s.logger.Error("channel accept", "err", err)
			continue
		}

		handler := &sessionHandler{
			srv:      s,
			channel:  channel,
			requests: requests,
			user:     sshConn.User(),
		}

		go handler.handle(ctx)
	}

	s.logger.Info("client disconnected", "user", sshConn.User(), "remote", sshConn.RemoteAddr().String())
	return nil
}

func loadOrCreateHostKey(path string) (ssh.Signer, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, fmt.Errorf("ensure host key directory: %w", err)
	}

	pemBytes, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			pemBytes, err = generateHostKey()
			if err != nil {
				return nil, err
			}
			if err := os.WriteFile(path, pemBytes, 0600); err != nil {
				return nil, fmt.Errorf("write host key: %w", err)
			}
		} else {
			return nil, fmt.Errorf("read host key: %w", err)
		}
	}

	signer, err := ssh.ParsePrivateKey(pemBytes)
	if err != nil {
		return nil, fmt.Errorf("parse host key: %w", err)
	}

	return signer, nil
}

func generateHostKey() ([]byte, error) {
	key, err := rsa.GenerateKey(rand.Reader, 4096)
	if err != nil {
		return nil, fmt.Errorf("generate rsa key: %w", err)
	}

	block := &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}
	return pem.EncodeToMemory(block), nil
}
