package api

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/mousebridge/core/internal/config"
	"github.com/mousebridge/core/internal/helper"
	"github.com/mousebridge/core/internal/localvalidate"
	"github.com/mousebridge/core/internal/remembered"
	"github.com/mousebridge/core/internal/validate"
)

func apiErr(code, msg string) gin.H {
	return gin.H{"error": gin.H{"code": code, "message": msg}}
}

func (s *Server) handleStatus(c *gin.Context) {
	c.JSON(http.StatusOK, s.d.State())
}

func (s *Server) handleLocalStatus(c *gin.Context) {
	c.JSON(http.StatusOK, s.d.LocalState())
}

func (s *Server) handleLocalHelperStatus(c *gin.Context) {
	c.JSON(http.StatusOK, s.d.LocalHelperStatus())
}

func (s *Server) handleLocalLabStatus(c *gin.Context) {
	c.JSON(http.StatusOK, s.d.LocalLabStatus())
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

func (s *Server) handleLocalLabStart(c *gin.Context) {
	if err := s.d.StartLocalLab(); err != nil {
		c.JSON(http.StatusBadRequest, apiErr("local_lab_start_failed", err.Error()))
		return
	}
	c.JSON(http.StatusAccepted, s.d.LocalLabStatus())
}

func (s *Server) handleLocalLabStop(c *gin.Context) {
	if err := s.d.StopLocalLab(); err != nil {
		c.JSON(http.StatusBadRequest, apiErr("local_lab_stop_failed", err.Error()))
		return
	}
	c.JSON(http.StatusOK, s.d.LocalLabStatus())
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

func (s *Server) handlePairApprove(c *gin.Context) {
	var req struct {
		PairingID string `json:"pairing_id"`
		Trusted   *bool  `json:"trusted"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.PairingID == "" {
		c.JSON(http.StatusBadRequest, apiErr("invalid_request", "pairing_id is required"))
		return
	}
	// Omitted trusted keeps backward compatibility with older clients, whose
	// only approval action meant permanent trust.
	trusted := true
	if req.Trusted != nil {
		trusted = *req.Trusted
	}
	if err := s.d.ApproveInbound(req.PairingID, trusted); err != nil {
		c.JSON(http.StatusBadRequest, apiErr("pair_approve_failed", err.Error()))
		return
	}
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
		Alias              *string `json:"alias"`
		TrustedAutoConnect *bool   `json:"trusted_auto_connect"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, apiErr("invalid_request", "invalid JSON body"))
		return
	}
	if req.Alias == nil && req.TrustedAutoConnect == nil {
		c.JSON(http.StatusBadRequest, apiErr("invalid_request", "at least one remembered field is required"))
		return
	}
	if req.Alias != nil {
		if err := validate.SafeString(*req.Alias); err != nil {
			c.JSON(http.StatusBadRequest, apiErr("invalid_request", "alias contains invalid characters"))
			return
		}
	}
	if err := s.d.RememberedPatch(deviceID, remembered.Patch{
		Alias:              req.Alias,
		TrustedAutoConnect: req.TrustedAutoConnect,
	}); err != nil {
		c.JSON(http.StatusNotFound, apiErr("not_found", err.Error()))
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (s *Server) handleRememberedSettingsPut(c *gin.Context) {
	var req struct {
		AutoConnectEnabled *bool `json:"auto_connect_enabled"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.AutoConnectEnabled == nil {
		c.JSON(http.StatusBadRequest, apiErr("invalid_request", "auto_connect_enabled is required"))
		return
	}
	if err := s.d.UpdateRememberedAutoConnectEnabled(*req.AutoConnectEnabled); err != nil {
		c.JSON(http.StatusInternalServerError, apiErr("internal_error", err.Error()))
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"ok":                   true,
		"auto_connect_enabled": s.d.Config().RememberedAutoConnectEnabled,
	})
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

func (s *Server) handleLayoutGet(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"edge_targets":           s.d.Config().EdgeTargets,
		"edge_layout_configured": s.d.Config().EdgeLayoutConfigured,
	})
}

func (s *Server) handleLayoutPut(c *gin.Context) {
	var req struct {
		EdgeTargets map[string]string `json:"edge_targets"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.EdgeTargets == nil {
		c.JSON(http.StatusBadRequest, apiErr("invalid_request", "edge_targets is required"))
		return
	}

	next := config.EdgeTargets{}
	assigned := make(map[string]string)
	for edge, deviceID := range req.EdgeTargets {
		switch edge {
		case "left", "right", "top", "bottom":
		default:
			c.JSON(http.StatusBadRequest, apiErr("invalid_request", "edge must be left, right, top, or bottom"))
			return
		}
		deviceID = strings.TrimSpace(deviceID)
		if deviceID == "" {
			continue
		}
		if err := validate.DeviceID(deviceID); err != nil {
			c.JSON(http.StatusBadRequest, apiErr("invalid_request", "edge target must be a valid device ID"))
			return
		}
		if previous, exists := assigned[deviceID]; exists {
			c.JSON(http.StatusBadRequest, apiErr("invalid_request", fmt.Sprintf("device is assigned to both %s and %s edges", previous, edge)))
			return
		}
		assigned[deviceID] = edge
		switch edge {
		case "left":
			next.Left = deviceID
		case "right":
			next.Right = deviceID
		case "top":
			next.Top = deviceID
		case "bottom":
			next.Bottom = deviceID
		}
	}

	if err := s.d.UpdateEdgeTargets(next); err != nil {
		c.JSON(http.StatusInternalServerError, apiErr("internal_error", err.Error()))
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "edge_targets": next, "edge_layout_configured": true})
}

func (s *Server) handlePointerGet(c *gin.Context) {
	mouseSpeed, scrollSpeed := s.d.PointerSpeeds()
	shakeEnabled, shakeSens := s.d.ShakeToLocateConfig()
	c.JSON(http.StatusOK, gin.H{
		"mouse_speed":             mouseSpeed,
		"scroll_speed":            scrollSpeed,
		"shake_to_locate_enabled": shakeEnabled,
		"shake_sensitivity":       shakeSens,
	})
}

func (s *Server) handlePointerPut(c *gin.Context) {
	var req struct {
		MouseSpeed           *float64 `json:"mouse_speed"`
		ScrollSpeed          *float64 `json:"scroll_speed"`
		ShakeToLocateEnabled *bool    `json:"shake_to_locate_enabled"`
		ShakeSensitivity     *string  `json:"shake_sensitivity"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, apiErr("invalid_request", "invalid JSON body"))
		return
	}
	if req.MouseSpeed == nil && req.ScrollSpeed == nil && req.ShakeToLocateEnabled == nil && req.ShakeSensitivity == nil {
		c.JSON(http.StatusBadRequest, apiErr("invalid_request", "mouse_speed, scroll_speed, shake_to_locate_enabled, or shake_sensitivity is required"))
		return
	}
	mouseSpeed, scrollSpeed := s.d.PointerSpeeds()
	var err error
	if req.MouseSpeed != nil || req.ScrollSpeed != nil {
		mouseSpeed, scrollSpeed, err = s.d.UpdatePointerSpeeds(req.MouseSpeed, req.ScrollSpeed)
		if err != nil {
			c.JSON(http.StatusBadRequest, apiErr("invalid_request", err.Error()))
			return
		}
	}
	shakeEnabled, shakeSens := s.d.ShakeToLocateConfig()
	if req.ShakeToLocateEnabled != nil || req.ShakeSensitivity != nil {
		shakeEnabled, shakeSens, err = s.d.UpdateShakeToLocate(req.ShakeToLocateEnabled, req.ShakeSensitivity)
		if err != nil {
			c.JSON(http.StatusBadRequest, apiErr("invalid_request", err.Error()))
			return
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"ok":                      true,
		"mouse_speed":             mouseSpeed,
		"scroll_speed":            scrollSpeed,
		"shake_to_locate_enabled": shakeEnabled,
		"shake_sensitivity":       shakeSens,
	})
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

func (s *Server) handleControlSwitchNext(c *gin.Context) {
	s.d.SwitchNext()
	state := s.d.State()
	c.JSON(http.StatusOK, gin.H{
		"ok":                      true,
		"active_target_device_id": state.ActiveTargetID,
		"controlling_remote":      state.ControllingRemote,
	})
}

func (s *Server) handleControlSwitchPrev(c *gin.Context) {
	s.d.SwitchPrev()
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

func (s *Server) handleKeepAwakePut(c *gin.Context) {
	var req struct {
		Enabled *bool `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Enabled == nil {
		c.JSON(http.StatusBadRequest, apiErr("invalid_request", "enabled is required"))
		return
	}
	status, err := s.d.UpdateKeepAwakeEnabled(*req.Enabled)
	if err != nil {
		c.JSON(http.StatusBadRequest, apiErr("keep_awake_unavailable", err.Error()))
		return
	}
	c.JSON(http.StatusOK, status)
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
	case "mouse_move", "mouse_move_abs", "mouse_button", "scroll", "key_tap", "key_down", "key_up", "text", "locate_cursor":
		return nil
	default:
		return fmt.Errorf("kind must be mouse_move, mouse_move_abs, mouse_button, scroll, key_tap, key_down, key_up, text, or locate_cursor")
	}
}

func validateSessionInputKind(kind string) error {
	switch kind {
	case "mouse_move", "mouse_move_abs", "mouse_button", "key_down", "key_up", "key_tap", "scroll", "text", "locate_cursor":
		return nil
	default:
		return fmt.Errorf("kind must be mouse_move, mouse_move_abs, mouse_button, key_down, key_up, key_tap, scroll, text, or locate_cursor")
	}
}

func (s *Server) handleClipboardGet(c *gin.Context) {
	c.JSON(http.StatusOK, s.d.ClipboardStatusSnapshot())
}

func (s *Server) handleClipboardSettingsPut(c *gin.Context) {
	var req struct {
		Enabled         *bool `json:"enabled"`
		AllowServerSync *bool `json:"allow_server_sync"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, apiErr("invalid_request", "invalid JSON body"))
		return
	}
	status := s.d.UpdateClipboardSettings(req.Enabled, req.AllowServerSync)
	c.JSON(http.StatusOK, status)
}

func (s *Server) handleClipboardSend(c *gin.Context) {
	var req struct {
		DeviceID string `json:"device_id"`
		Text     string `json:"text"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, apiErr("invalid_request", "invalid JSON body"))
		return
	}
	status, err := s.d.SendClipboard(req.DeviceID, req.Text)
	if err != nil {
		c.JSON(http.StatusBadRequest, apiErr("clipboard_send_failed", err.Error()))
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "clipboard": status})
}

func (s *Server) handleClipboardHistoryGet(c *gin.Context) {
	c.JSON(http.StatusOK, s.d.ClipboardHistorySnapshot())
}

func (s *Server) handleClipboardHistorySettingsPut(c *gin.Context) {
	var req struct {
		Enabled *bool `json:"enabled"`
		Limit   *int  `json:"limit"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Enabled == nil {
		c.JSON(http.StatusBadRequest, apiErr("invalid_request", "enabled is required"))
		return
	}
	limit := s.d.Config().ClipboardHistoryLimit
	if req.Limit != nil {
		limit = *req.Limit
	}
	status, err := s.d.UpdateClipboardHistorySettings(*req.Enabled, limit)
	if err != nil {
		c.JSON(http.StatusBadRequest, apiErr("clipboard_history_settings_failed", err.Error()))
		return
	}
	c.JSON(http.StatusOK, status)
}

func (s *Server) handleClipboardHistoryClear(c *gin.Context) {
	if err := s.d.ClearClipboardHistory(); err != nil {
		c.JSON(http.StatusInternalServerError, apiErr("clipboard_history_clear_failed", err.Error()))
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (s *Server) handleClipboardHistoryPaste(c *gin.Context) {
	entryID := c.Param("entry_id")
	if err := validate.SafeString(entryID); err != nil || entryID == "" {
		c.JSON(http.StatusBadRequest, apiErr("invalid_request", "entry_id is invalid"))
		return
	}
	if err := s.d.PasteClipboardHistory(entryID); err != nil {
		c.JSON(http.StatusBadRequest, apiErr("clipboard_history_paste_failed", err.Error()))
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"ok": true})
}

func (s *Server) handleQuickTextGet(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"entries": s.d.QuickTextEntries()})
}

func (s *Server) handleQuickTextPut(c *gin.Context) {
	var req struct {
		Entries []config.QuickText `json:"entries"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Entries == nil {
		c.JSON(http.StatusBadRequest, apiErr("invalid_request", "entries is required"))
		return
	}
	entries, err := s.d.UpdateQuickTexts(req.Entries)
	if err != nil {
		c.JSON(http.StatusBadRequest, apiErr("quick_text_update_failed", err.Error()))
		return
	}
	c.JSON(http.StatusOK, gin.H{"entries": entries})
}

func (s *Server) handleFilesGet(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"download_dir": s.d.FileDownloadDir(),
		"transfers":    s.d.FileTransfersSnapshot(),
	})
}

func (s *Server) handleFilesSettingsPut(c *gin.Context) {
	var req struct {
		DownloadDir string `json:"download_dir"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.DownloadDir) == "" {
		c.JSON(http.StatusBadRequest, apiErr("invalid_request", "download_dir is required"))
		return
	}
	resolved, err := s.d.SetFileDownloadDir(req.DownloadDir)
	if err != nil {
		c.JSON(http.StatusBadRequest, apiErr("invalid_request", err.Error()))
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "download_dir": resolved})
}

func (s *Server) handleFilesSend(c *gin.Context) {
	var req struct {
		DeviceID   string `json:"device_id"`
		FilePath   string `json:"file_path"`
		FileName   string `json:"file_name"`
		DataBase64 string `json:"data_base64"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, apiErr("invalid_request", "invalid JSON body"))
		return
	}

	if strings.TrimSpace(req.FilePath) != "" {
		st, err := s.d.SendFileFromPath(req.DeviceID, req.FilePath)
		if err != nil {
			c.JSON(http.StatusBadRequest, apiErr("file_send_failed", err.Error()))
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "transfer": st})
		return
	}

	if strings.TrimSpace(req.FileName) == "" {
		c.JSON(http.StatusBadRequest, apiErr("invalid_request", "file_path or file_name is required"))
		return
	}
	raw, err := base64.StdEncoding.DecodeString(req.DataBase64)
	if err != nil {
		c.JSON(http.StatusBadRequest, apiErr("invalid_request", "invalid data_base64"))
		return
	}
	st, err := s.d.SendFileFromBytes(req.DeviceID, req.FileName, raw)
	if err != nil {
		c.JSON(http.StatusBadRequest, apiErr("file_send_failed", err.Error()))
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "transfer": st})
}

func (s *Server) handleFilesSendFolder(c *gin.Context) {
	var req struct {
		DeviceID   string `json:"device_id"`
		FolderName string `json:"folder_name"`
		Files      []struct {
			FileName   string `json:"file_name"`
			DataBase64 string `json:"data_base64"`
		} `json:"files"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.FolderName) == "" || len(req.Files) == 0 {
		c.JSON(http.StatusBadRequest, apiErr("invalid_request", "folder_name and files are required"))
		return
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	root := filepath.Base(filepath.Clean(req.FolderName))
	for _, file := range req.Files {
		name := filepath.ToSlash(filepath.Clean(file.FileName))
		if name == "." || strings.HasPrefix(name, "../") || strings.HasPrefix(name, "/") {
			_ = zw.Close()
			c.JSON(http.StatusBadRequest, apiErr("invalid_request", "invalid folder entry path"))
			return
		}
		data, err := base64.StdEncoding.DecodeString(file.DataBase64)
		if err != nil {
			_ = zw.Close()
			c.JSON(http.StatusBadRequest, apiErr("invalid_request", "invalid folder file data"))
			return
		}
		entry, err := zw.Create(filepath.ToSlash(filepath.Join(root, name)))
		if err != nil {
			_ = zw.Close()
			c.JSON(http.StatusBadRequest, apiErr("file_send_failed", err.Error()))
			return
		}
		if _, err = entry.Write(data); err != nil {
			_ = zw.Close()
			c.JSON(http.StatusBadRequest, apiErr("file_send_failed", err.Error()))
			return
		}
	}
	if err := zw.Close(); err != nil {
		c.JSON(http.StatusBadRequest, apiErr("file_send_failed", err.Error()))
		return
	}
	st, err := s.d.SendFileFromBytes(req.DeviceID, root+".zip", buf.Bytes())
	if err != nil {
		c.JSON(http.StatusBadRequest, apiErr("file_send_failed", err.Error()))
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "transfer": st})
}
