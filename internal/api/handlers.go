package api

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/mousebridge/core/internal/validate"
)

func apiErr(code, msg string) gin.H {
	return gin.H{"error": gin.H{"code": code, "message": msg}}
}

func (s *Server) handleStatus(c *gin.Context) {
	c.JSON(http.StatusOK, s.d.State())
}

func (s *Server) handleConnect(c *gin.Context) {
	var req struct {
		Host string `json:"host"`
		Port int    `json:"port"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, apiErr("invalid_request", "invalid JSON body"))
		return
	}
	if err := validate.ConnectRequest(req.Host, req.Port); err != nil {
		c.JSON(http.StatusBadRequest, apiErr("invalid_request", err.Error()))
		return
	}
	s.d.Connect(req.Host, req.Port)
	c.JSON(http.StatusAccepted, gin.H{"ok": true})
}

func (s *Server) handlePairPIN(c *gin.Context) {
	var req struct {
		PairingID string `json:"pairing_id"`
		PIN       string `json:"pin"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.PairingID == "" || req.PIN == "" {
		c.JSON(http.StatusBadRequest, apiErr("invalid_request", "pairing_id and pin are required"))
		return
	}
	if err := s.d.SendPIN(req.PairingID, req.PIN); err != nil {
		c.JSON(http.StatusBadRequest, apiErr("not_found", err.Error()))
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (s *Server) handlePairReject(c *gin.Context) {
	var req struct {
		PairingID string `json:"pairing_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.PairingID == "" {
		c.JSON(http.StatusBadRequest, apiErr("invalid_request", "pairing_id is required"))
		return
	}
	// Try both inbound (server) and outbound (client) sides.
	s.d.RejectInbound(req.PairingID)
	s.d.RejectOutbound(req.PairingID)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (s *Server) handleRememberedList(c *gin.Context) {
	c.JSON(http.StatusOK, s.d.RememberedList())
}

func (s *Server) handleRememberedDelete(c *gin.Context) {
	deviceID := c.Param("device_id")
	if err := validate.DeviceID(deviceID); err != nil {
		c.JSON(http.StatusBadRequest, apiErr("invalid_request", err.Error()))
		return
	}
	if err := s.d.RememberedForget(deviceID); err != nil {
		c.JSON(http.StatusNotFound, apiErr("not_found", err.Error()))
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (s *Server) handleRememberedRename(c *gin.Context) {
	deviceID := c.Param("device_id")
	if err := validate.DeviceID(deviceID); err != nil {
		c.JSON(http.StatusBadRequest, apiErr("invalid_request", err.Error()))
		return
	}
	var req struct {
		Alias string `json:"alias"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, apiErr("invalid_request", "invalid JSON body"))
		return
	}
	if err := validate.SafeString(req.Alias); err != nil {
		c.JSON(http.StatusBadRequest, apiErr("invalid_request", "alias contains invalid characters"))
		return
	}
	if err := s.d.RememberedRename(deviceID, req.Alias); err != nil {
		c.JSON(http.StatusNotFound, apiErr("not_found", err.Error()))
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
