package api

import (
	"context"
	"net"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/mousebridge/core/internal/daemon"
	"github.com/mousebridge/core/internal/shortcuts"
)

// Server is the HTTP API server for browser/UI clients.
type Server struct {
	d         *daemon.Daemon
	hub       *sseHub
	shortcuts *shortcuts.Manager
	srv       *http.Server
}

// New creates a Server backed by the given daemon.
func New(d *daemon.Daemon) *Server {
	return &Server{
		d:         d,
		hub:       newSSEHub(),
		shortcuts: shortcuts.New(),
	}
}

// Start begins serving on the provided listener.
func (s *Server) Start(ln net.Listener) {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(corsMiddleware())
	s.routes(r)

	s.srv = &http.Server{Handler: r}
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
	api.POST("/serve", s.handleServe)
	api.POST("/connect", s.handleConnect)
	api.POST("/stop-serve", s.handleStopServe)
	api.POST("/disconnect", s.handleDisconnect)
	api.POST("/pair/accept", s.handlePairAccept)
	api.POST("/pair/reject", s.handlePairReject)
	api.POST("/pair/pin", s.handlePairPIN)
	api.GET("/status", s.handleStatus)
	api.GET("/devices", s.handleDevices)
	api.GET("/shortcuts", s.handleShortcuts)
	api.PUT("/shortcuts", s.handleShortcuts)
	api.GET("/trusted", s.handleTrust)
	api.POST("/trusted", s.handleTrust)
	api.DELETE("/trusted", s.handleTrust)
	api.GET("/events", s.handleSSE)
}

func (s *Server) fanOutEvents() {
	ch := s.d.Subscribe()
	defer s.d.Unsubscribe(ch)
	for ev := range ch {
		s.hub.broadcast(ev)
	}
}

// NewChanListener wraps a channel of net.Conn as a net.Listener.
// Used to feed HTTP connections dispatched by transport.Serve into the Gin server.
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
		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Content-Type")
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}
