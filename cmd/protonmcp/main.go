// Command protonmcp is an MCP server for Proton Mail via Proton Mail Bridge.
// It can read, search, organise and draft mail. It cannot send mail.
package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/henrrrik/proton-mail-mcp/internal/config"
	"github.com/henrrrik/proton-mail-mcp/internal/mail"
	"github.com/henrrrik/proton-mail-mcp/internal/policy"
	"github.com/henrrrik/proton-mail-mcp/internal/tools"
)

var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "protonmcp:", err)
		os.Exit(1)
	}
}

func run() error {
	configPath := flag.String("config", os.Getenv("PROTONMCP_CONFIG"), "optional TOML config file")
	httpAddr := flag.String("http", "", "serve streamable HTTP on this loopback address instead of stdio, e.g. 127.0.0.1:8765")
	flag.Parse()

	// stdout carries the MCP protocol; logs go to stderr.
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	cfg, err := config.Load(*configPath, os.Getenv)
	if err != nil {
		return err
	}
	tlsCfg, err := mail.TLSConfig(cfg.BridgeCert, mail.HostOf(cfg.IMAPAddr), cfg.InsecureLoopback)
	if err != nil {
		return err
	}
	gate, err := policy.New(policy.Options{
		ReadOnly:     cfg.ReadOnly,
		AllowFolders: cfg.AllowFolders,
		DenyFolders:  cfg.DenyFolders,
		AuditLog:     cfg.AuditLog,
	})
	if err != nil {
		return err
	}
	defer gate.Close()
	mc := mail.New(mail.Options{Addr: cfg.IMAPAddr, User: cfg.User, Password: cfg.Password, TLS: tlsCfg})
	defer mc.Close()

	server := tools.NewServer(version, tools.Deps{Gate: gate, Mail: mc, Self: cfg.User})
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Info("starting", "version", version, "read_only", cfg.ReadOnly, "imap", cfg.IMAPAddr)
	if *httpAddr == "" {
		return server.Run(ctx, &mcp.StdioTransport{})
	}
	return serveHTTP(ctx, *httpAddr, server, log)
}

func serveHTTP(ctx context.Context, addr string, server *mcp.Server, log *slog.Logger) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("-http: %w", err)
	}
	if !config.IsLoopback(host) {
		return fmt.Errorf("-http: %q is not a loopback address", addr)
	}
	// Loopback is not a trust boundary: any local process or user could
	// connect. Require a bearer token, from the environment or freshly made.
	token := os.Getenv("PROTONMCP_HTTP_TOKEN")
	if token == "" {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return err
		}
		token = hex.EncodeToString(b)
		fmt.Fprintf(os.Stderr, "HTTP bearer token (set PROTONMCP_HTTP_TOKEN to choose your own): %s\n", token)
	}
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{
		SessionTimeout:      30 * time.Minute,
		MaxRequestBodyBytes: 1 << 20,
	})
	protected := http.NewCrossOriginProtection().Handler(mcpHandler)
	want := []byte("Bearer " + token)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), want) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		protected.ServeHTTP(w, r)
	})
	srv := &http.Server{Addr: addr, Handler: handler, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: time.Minute, IdleTimeout: 5 * time.Minute}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdown)
	}()
	log.Info("listening", "addr", addr)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
