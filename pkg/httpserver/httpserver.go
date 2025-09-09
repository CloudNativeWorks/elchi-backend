package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/CloudNativeWorks/elchi-backend/pkg/config"
	"github.com/CloudNativeWorks/elchi-backend/pkg/logger"
)

type Server struct {
	Router *gin.Engine
}

func NewHTTPServer(router *gin.Engine) *Server {
	return &Server{
		Router: router,
	}
}

func (s *Server) Run(config *config.AppConfig, logger *logger.Logger) error {
	port := 8099
	addr := fmt.Sprintf(":%d", port)
	server := &http.Server{
		Addr:              addr,
		Handler:           s.Router,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,  // Allow 30s for reading request
		WriteTimeout:      45 * time.Second,  // Allow 45s for writing response (includes forward time)
		IdleTimeout:       60 * time.Second,  // Keep connections alive for 60s
	}
	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Fatalf("listen: %s\n", err)
		}
	}()

	logger.Infof("Starting http web server [::]:%d", port)
	<-done
	logger.Info("Http web server stop signal received")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := server.Shutdown(ctx)
	if err != nil {
		return fmt.Errorf("server shutdown failed: %w", err)
	}
	logger.Print("Server exited properly")

	if errors.Is(err, http.ErrServerClosed) {
		err = nil
	}

	return err
}
