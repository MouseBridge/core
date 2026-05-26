package api

import (
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"

	"github.com/mousebridge/core/internal/config"
	"github.com/mousebridge/core/internal/daemon"
)

func (s *Server) handleServe(c *gin.Context) {
	var req struct {
		Port int `json:"port"`
	}
	_ = c.ShouldBindJSON(&req)
	s.d.HandleCommand(daemon.Command{Cmd: "serve", Port: req.Port})
	c.JSON(http.StatusOK, gin.H{"ok": "true"})
}

func (s *Server) handleConnect(c *gin.Context) {
	var req struct {
		IP   string `json:"ip"`
		Port int    `json:"port"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.IP == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ip required"})
		return
	}
	s.d.HandleCommand(daemon.Command{Cmd: "connect", IP: req.IP, Port: req.Port})
	c.JSON(http.StatusOK, gin.H{"ok": "true"})
}

func (s *Server) handleStopServe(c *gin.Context) {
	s.d.HandleCommand(daemon.Command{Cmd: "stop_serve"})
	c.JSON(http.StatusOK, gin.H{"ok": "true"})
}

func (s *Server) handleDisconnect(c *gin.Context) {
	var req struct {
		DeviceID string `json:"device_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.DeviceID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "device_id required"})
		return
	}
	s.d.HandleCommand(daemon.Command{Cmd: "disconnect", DeviceID: req.DeviceID})
	c.JSON(http.StatusOK, gin.H{"ok": "true"})
}

func (s *Server) handlePairAccept(c *gin.Context) {
	var req struct {
		DeviceID string `json:"device_id"`
	}
	_ = c.ShouldBindJSON(&req)
	s.d.HandleCommand(daemon.Command{Cmd: "pair_accept", DeviceID: req.DeviceID})
	c.JSON(http.StatusOK, gin.H{"ok": "true"})
}

func (s *Server) handlePairReject(c *gin.Context) {
	var req struct {
		DeviceID string `json:"device_id"`
	}
	_ = c.ShouldBindJSON(&req)
	s.d.HandleCommand(daemon.Command{Cmd: "pair_reject", DeviceID: req.DeviceID})
	c.JSON(http.StatusOK, gin.H{"ok": "true"})
}

func (s *Server) handlePairPIN(c *gin.Context) {
	var req struct {
		PIN string `json:"pin"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.PIN == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "pin required"})
		return
	}
	s.d.HandleCommand(daemon.Command{Cmd: "pair_pin", PIN: req.PIN})
	c.JSON(http.StatusOK, gin.H{"ok": "true"})
}

func (s *Server) handleStatus(c *gin.Context) {
	c.JSON(http.StatusOK, s.d.State())
}

func (s *Server) handleTrust(c *gin.Context) {
	switch c.Request.Method {
	case http.MethodGet:
		c.JSON(http.StatusOK, s.d.TrustedList())
	case http.MethodPost:
		var req struct {
			DeviceID string `json:"device_id"`
			Name     string `json:"name"`
		}
		if err := c.ShouldBindJSON(&req); err != nil || req.DeviceID == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "device_id required"})
			return
		}
		s.d.HandleCommand(daemon.Command{Cmd: "trust", DeviceID: req.DeviceID, Name: req.Name})
		c.JSON(http.StatusOK, gin.H{"ok": "true"})
	case http.MethodDelete:
		var req struct {
			DeviceID string `json:"device_id"`
		}
		if err := c.ShouldBindJSON(&req); err != nil || req.DeviceID == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "device_id required"})
			return
		}
		s.d.HandleCommand(daemon.Command{Cmd: "untrust", DeviceID: req.DeviceID})
		c.JSON(http.StatusOK, gin.H{"ok": "true"})
	default:
		c.Status(http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleShortcuts(c *gin.Context) {
	switch c.Request.Method {
	case http.MethodGet:
		c.JSON(http.StatusOK, s.d.Config().Hotkeys)
	case http.MethodPut:
		var h config.Hotkeys
		if err := c.ShouldBindJSON(&h); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid json"})
			return
		}
		if err := s.d.UpdateHotkeys(h); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		_ = s.shortcuts.Push(h)
		c.JSON(http.StatusOK, gin.H{"ok": "true"})
	default:
		c.Status(http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleSSE(c *gin.Context) {
	c.Header("Cache-Control", "no-cache")
	c.Header("X-Accel-Buffering", "no")

	ch := make(chan daemon.Event, 64)
	s.hub.add(ch)
	defer s.hub.remove(ch)

	log.Printf("api: SSE client connected from %s", c.Request.RemoteAddr)

	c.Stream(func(w io.Writer) bool {
		select {
		case <-c.Request.Context().Done():
			return false
		case ev, ok := <-ch:
			if !ok {
				return false
			}
			c.SSEvent("message", ev)
			return true
		}
	})

	log.Printf("api: SSE client disconnected from %s", c.Request.RemoteAddr)
}
