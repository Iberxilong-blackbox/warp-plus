package app

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"time"
)

const defaultRotateControlPath = "/connectivity/refresh"

func startRotateControlServer(ctx context.Context, l *slog.Logger, opts *RotateControlOptions, pool *childPool) error {
	path := opts.Path
	if path == "" {
		path = defaultRotateControlPath
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		writeControlStatus(w, http.StatusOK, "ok")
	})
	mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeControlStatus(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		if opts.Token != "" && r.Header.Get("Authorization") != "Bearer "+opts.Token {
			writeControlStatus(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		status := pool.rotate(r.Context())
		switch status {
		case rotateStatusOK:
			writeControlStatus(w, http.StatusOK, string(status))
		case rotateStatusBusy:
			writeControlStatus(w, http.StatusConflict, string(status))
		case rotateStatusNoAcceptableEgress:
			writeControlStatus(w, http.StatusServiceUnavailable, string(status))
		default:
			writeControlStatus(w, http.StatusInternalServerError, string(rotateStatusRefreshFailed))
		}
	})

	server := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
	}

	listener, err := net.Listen("tcp", opts.Bind.String())
	if err != nil {
		return err
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	go func() {
		l.Info("serving rotate control", "address", listener.Addr().String(), "path", path)
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			l.Error("rotate control server failed", "error", err)
		}
	}()

	return nil
}

func writeControlStatus(w http.ResponseWriter, code int, status string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": status})
}
