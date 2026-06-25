package api

import (
	"context"
	"net"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/mousebridge/core/internal/daemon"
)

// Server is the HTTP API server for browser/UI clients.
type Server struct {
	d   *daemon.Daemon
	hub *sseHub
	srv *http.Server
}

// New creates a Server backed by the given daemon.
func New(d *daemon.Daemon) *Server {
	return &Server{
		d:   d,
		hub: newSSEHub(),
	}
}

// Start begins serving on the provided listener.
func (s *Server) Start(ln net.Listener) {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(corsMiddleware())
	r.Use(bodyLimitMiddleware(s.d.Config().JSONBodyLimitBytes))
	s.routes(r)

	s.srv = &http.Server{
		Handler:           r,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		IdleTimeout:       60 * time.Second,
		// WriteTimeout intentionally 0: SSE is a long-lived stream.
	}
	go s.fanOutEvents()
	go s.srv.Serve(ln) //nolint:errcheck
}

// Stop shuts down the HTTP server gracefully.
func (s *Server) Stop() {
	if s.srv == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = s.srv.Shutdown(ctx)
}

func (s *Server) routes(r *gin.Engine) {
	api := r.Group("/api")
	api.GET("/status", s.handleStatus)
	api.GET("/local/helper", s.handleLocalHelperStatus)
	api.GET("/events", s.handleSSE)
	api.POST("/connect", s.handleConnect)
	api.POST("/local/helper/install", s.handleLocalHelperInstall)
	api.POST("/local/helper/restart", s.handleLocalHelperRestart)
	api.POST("/local/helper/open-accessibility", s.handleLocalHelperOpenAccessibility)
	api.DELETE("/sessions/:device_id", s.handleSessionDelete)
	api.POST("/pair/pin", s.handlePairPIN)
	api.POST("/pair/reject", s.handlePairReject)
	api.GET("/remembered", s.handleRememberedList)
	api.DELETE("/remembered/:device_id", s.handleRememberedDelete)
	api.PATCH("/remembered/:device_id", s.handleRememberedRename)
	api.GET("/shortcuts", s.handleShortcutsGet)
	api.PUT("/shortcuts", s.handleShortcutsPut)
	api.POST("/control/switch-to-host", s.handleControlSwitchToHost)
	api.POST("/control/disconnect-all", s.handleControlDisconnectAll)
	api.POST("/control/toggle-pause", s.handleControlTogglePause)
	api.POST("/helper/input", s.handleHelperInput)
	api.POST("/helper/input/batch", s.handleHelperInputBatch)
	api.POST("/session/input", s.handleSessionInput)
	api.POST("/session/input/batch", s.handleSessionInputBatch)
}

func (s *Server) fanOutEvents() {
	ch := s.d.Subscribe()
	defer s.d.Unsubscribe(ch)
	for ev := range ch {
		s.hub.broadcast(ev)
	}
}

// NewChanListener wraps a channel of net.Conn as a net.Listener.
func NewChanListener(ch <-chan net.Conn, addr net.Addr) net.Listener {
	return &chanListener{ch: ch, addr: addr}
}

type chanListener struct {
	ch   <-chan net.Conn
	addr net.Addr
}

func (l *chanListener) Accept() (net.Conn, error) {
	c, ok := <-l.ch
	if !ok {
		return nil, net.ErrClosed
	}
	return c, nil
}

func (l *chanListener) Close() error   { return nil }
func (l *chanListener) Addr() net.Addr { return l.addr }

func corsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Content-Type")
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

func bodyLimitMiddleware(limit int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, limit)
		c.Next()
	}
}
