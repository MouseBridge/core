package api

import (
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/mousebridge/core/internal/config"
	"github.com/mousebridge/core/internal/helper"
	"github.com/mousebridge/core/internal/localvalidate"
	"github.com/mousebridge/core/internal/validate"
)

func apiErr(code, msg string) gin.H {
	return gin.H{"error": gin.H{"code": code, "message": msg}}
}

func (s *Server) handleStatus(c *gin.Context) {
	c.JSON(http.StatusOK, s.d.State())
}

func (s *Server) handleLocalHelperStatus(c *gin.Context) {
	c.JSON(http.StatusOK, s.d.LocalHelperStatus())
}

func (s *Server) handleLocalValidationStatus(c *gin.Context) {
	c.JSON(http.StatusOK, s.d.LocalValidationStatus())
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

func (s *Server) handleLocalHelperInstall(c *gin.Context) {
	if err := s.d.InstallLocalHelper(); err != nil {
		c.JSON(http.StatusBadRequest, apiErr("helper_install_failed", err.Error()))
		return
	}
	c.JSON(http.StatusOK, s.d.LocalHelperStatus())
}

func (s *Server) handleLocalHelperRestart(c *gin.Context) {
	if err := s.d.RestartLocalHelper(); err != nil {
		c.JSON(http.StatusBadRequest, apiErr("helper_restart_failed", err.Error()))
		return
	}
	c.JSON(http.StatusOK, s.d.LocalHelperStatus())
}

func (s *Server) handleLocalHelperOpenAccessibility(c *gin.Context) {
	if err := s.d.OpenLocalHelperAccessibility(); err != nil {
		c.JSON(http.StatusBadRequest, apiErr("helper_accessibility_failed", err.Error()))
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (s *Server) handleLocalValidationRun(c *gin.Context) {
	var req localvalidate.RunRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, apiErr("invalid_request", "invalid JSON body"))
		return
	}
	if err := s.d.StartLocalValidation(req); err != nil {
		c.JSON(http.StatusBadRequest, apiErr("validation_run_failed", err.Error()))
		return
	}
	c.JSON(http.StatusAccepted, s.d.LocalValidationStatus())
}

func (s *Server) handleLocalValidationStop(c *gin.Context) {
	if err := s.d.StopLocalValidation(); err != nil {
		c.JSON(http.StatusBadRequest, apiErr("validation_stop_failed", err.Error()))
		return
	}
	c.JSON(http.StatusOK, s.d.LocalValidationStatus())
}

func (s *Server) handleSessionDelete(c *gin.Context) {
	deviceID := c.Param("device_id")
	if err := validate.DeviceID(deviceID); err != nil {
		c.JSON(http.StatusBadRequest, apiErr("invalid_request", err.Error()))
		return
	}
	if err := s.d.DisconnectDevice(deviceID); err != nil {
		c.JSON(http.StatusNotFound, apiErr("not_found", err.Error()))
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
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

func (s *Server) handleShortcutsGet(c *gin.Context) {
	c.JSON(http.StatusOK, s.d.Config().Hotkeys)
}

func (s *Server) handleShortcutsPut(c *gin.Context) {
	var req config.Hotkeys
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, apiErr("invalid_request", "invalid JSON body"))
		return
	}
	if err := s.d.UpdateHotkeys(req); err != nil {
		c.JSON(http.StatusInternalServerError, apiErr("internal_error", err.Error()))
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (s *Server) handleControlSwitchToHost(c *gin.Context) {
	s.d.SwitchToHost()
	state := s.d.State()
	c.JSON(http.StatusOK, gin.H{
		"ok":                      true,
		"active_target_device_id": state.ActiveTargetID,
		"controlling_remote":      state.ControllingRemote,
	})
}

func (s *Server) handleControlDisconnectAll(c *gin.Context) {
	s.d.DisconnectAll()
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (s *Server) handleControlTogglePause(c *gin.Context) {
	paused := s.d.TogglePause()
	c.JSON(http.StatusOK, gin.H{"ok": true, "paused": paused})
}

func (s *Server) handleControlCapturePut(c *gin.Context) {
	var req struct {
		Enabled *bool `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Enabled == nil {
		c.JSON(http.StatusBadRequest, apiErr("invalid_request", "enabled is required"))
		return
	}
	enabled := s.d.SetCaptureEnabled(*req.Enabled)
	c.JSON(http.StatusOK, gin.H{"ok": true, "capture_enabled": enabled})
}

func (s *Server) handleHelperInput(c *gin.Context) {
	var req helper.InputPayload
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, apiErr("invalid_request", "invalid JSON body"))
		return
	}

	if err := validateHelperInputKind(req.Kind); err != nil {
		c.JSON(http.StatusBadRequest, apiErr("invalid_request", err.Error()))
		return
	}
	if err := validateInputPayload(req); err != nil {
		c.JSON(http.StatusBadRequest, apiErr("invalid_request", err.Error()))
		return
	}

	s.d.PushHelperInput(req)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (s *Server) handleHelperInputBatch(c *gin.Context) {
	var req struct {
		Inputs      []helper.InputPayload `json:"inputs"`
		StepDelayMs int                   `json:"step_delay_ms"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, apiErr("invalid_request", "invalid JSON body"))
		return
	}
	if err := validateBatch(req.Inputs, req.StepDelayMs, true); err != nil {
		c.JSON(http.StatusBadRequest, apiErr("invalid_request", err.Error()))
		return
	}
	for _, input := range req.Inputs {
		s.d.PushHelperInput(input)
		if req.StepDelayMs > 0 {
			time.Sleep(time.Duration(req.StepDelayMs) * time.Millisecond)
		}
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "count": len(req.Inputs)})
}

func (s *Server) handleSessionInput(c *gin.Context) {
	var req struct {
		DeviceID string `json:"device_id"`
		helper.InputPayload
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, apiErr("invalid_request", "invalid JSON body"))
		return
	}

	if err := validateSessionInputKind(req.Kind); err != nil {
		c.JSON(http.StatusBadRequest, apiErr("invalid_request", err.Error()))
		return
	}
	if err := validateInputPayload(req.InputPayload); err != nil {
		c.JSON(http.StatusBadRequest, apiErr("invalid_request", err.Error()))
		return
	}

	if err := s.d.SendSessionInput(req.DeviceID, req.InputPayload); err != nil {
		c.JSON(http.StatusBadRequest, apiErr("not_found", err.Error()))
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (s *Server) handleSessionInputBatch(c *gin.Context) {
	var req struct {
		DeviceID    string                `json:"device_id"`
		Inputs      []helper.InputPayload `json:"inputs"`
		StepDelayMs int                   `json:"step_delay_ms"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, apiErr("invalid_request", "invalid JSON body"))
		return
	}
	if err := validateBatch(req.Inputs, req.StepDelayMs, false); err != nil {
		c.JSON(http.StatusBadRequest, apiErr("invalid_request", err.Error()))
		return
	}
	for _, input := range req.Inputs {
		if err := s.d.SendSessionInput(req.DeviceID, input); err != nil {
			c.JSON(http.StatusBadRequest, apiErr("not_found", err.Error()))
			return
		}
		if req.StepDelayMs > 0 {
			time.Sleep(time.Duration(req.StepDelayMs) * time.Millisecond)
		}
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "count": len(req.Inputs)})
}

func validateBatch(inputs []helper.InputPayload, stepDelayMs int, helperOnly bool) error {
	if len(inputs) == 0 {
		return fmt.Errorf("inputs must not be empty")
	}
	if stepDelayMs < 0 {
		return fmt.Errorf("step_delay_ms must be >= 0")
	}
	for _, input := range inputs {
		var err error
		if helperOnly {
			err = validateHelperInputKind(input.Kind)
		} else {
			err = validateSessionInputKind(input.Kind)
		}
		if err != nil {
			return err
		}
		if err := validateInputPayload(input); err != nil {
			return err
		}
	}
	return nil
}

func validateInputPayload(input helper.InputPayload) error {
	switch input.Kind {
	case "mouse_move_abs":
		if input.X == nil || input.Y == nil {
			return fmt.Errorf("mouse_move_abs requires x and y")
		}
	}
	return nil
}

func validateHelperInputKind(kind string) error {
	switch kind {
	case "mouse_move", "mouse_move_abs", "mouse_button", "scroll", "key_tap", "key_down", "key_up", "text":
		return nil
	default:
		return fmt.Errorf("kind must be mouse_move, mouse_move_abs, mouse_button, scroll, key_tap, key_down, key_up, or text")
	}
}

func validateSessionInputKind(kind string) error {
	switch kind {
	case "mouse_move", "mouse_move_abs", "mouse_button", "key_down", "key_up", "key_tap", "scroll", "text":
		return nil
	default:
		return fmt.Errorf("kind must be mouse_move, mouse_move_abs, mouse_button, key_down, key_up, key_tap, scroll, or text")
	}
}
