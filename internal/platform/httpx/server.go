package httpx

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"

	"github.com/Exonical/custos/internal/platform/config"
)

// NewServer builds an *http.Server from config. TLS mode "required" loads
// cert/key and enforces TLS 1.3; "disabled"/"upstream" serve plain HTTP.
func NewServer(cfg config.Server, h http.Handler, logger *slog.Logger) (*http.Server, error) {
	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           h,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelError),
	}
	if cfg.TLS.Mode == "required" {
		cert, err := tls.LoadX509KeyPair(cfg.TLS.CertFile, cfg.TLS.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("load TLS keypair: %w", err)
		}
		tlsCfg := &tls.Config{
			MinVersion:   tls.VersionTLS13,
			Certificates: []tls.Certificate{cert},
		}
		if cfg.TLS.RequireClientCert && cfg.TLS.ClientCAFile != "" {
			pool, err := loadCertPool(cfg.TLS.ClientCAFile)
			if err != nil {
				return nil, err
			}
			tlsCfg.ClientAuth = tls.RequireAndVerifyClientCert
			tlsCfg.ClientCAs = pool
		}
		srv.TLSConfig = tlsCfg
	}
	return srv, nil
}

func loadCertPool(path string) (*x509.CertPool, error) {
	pem, err := os.ReadFile(path) // #nosec G304 -- path is operator-supplied config
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("client CA file %s: no certificates found", path)
	}
	return pool, nil
}

// Serve runs srv until ctx is done, then gracefully shuts down within
// cfg.ShutdownTimeout. Returns nil on clean shutdown.
func Serve(ctx context.Context, srv *http.Server, cfg config.Server) error {
	errCh := make(chan error, 1)
	go func() {
		var err error
		if srv.TLSConfig != nil {
			err = srv.ListenAndServeTLS("", "")
		} else {
			err = srv.ListenAndServe()
		}
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		errCh <- err
	}()
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	return <-errCh
}
