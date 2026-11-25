package event

// P2P wire message types (v1.5.2-final-mvp protocol).
const (
	// Connection phase
	TypeHello = "hello"

	// Pairing phase
	TypePairChallenge       = "pair_challenge"
	TypePairConfirm         = "pair_confirm"
	TypePairApprove         = "pair_approve"
	TypePairAccept          = "pair_accept"
	TypePairReject          = "pair_reject"
	TypePairRetry           = "pair_retry"
	TypeRememberedChallenge = "remembered_challenge"
	TypeRememberedProof     = "remembered_proof"

	// Session phase
	TypePing          = "ping"
	TypePong          = "pong"
	TypeMouseMove     = "mouse_move"
	TypeMouseMoveAbs  = "mouse_move_abs"
	TypeMouseButton   = "mouse_button"
	TypeKeyDown       = "key_down"
	TypeKeyUp         = "key_up"
	TypeText          = "text"
	TypeScroll        = "scroll"
	TypeLocateCursor  = "locate_cursor"
	TypeSwitchRequest = "switch_request"
	TypeSwitchAck     = "switch_ack"
	TypeSwitchBack    = "switch_back"
	TypeClipboardSync = "clipboard_sync"
	TypeFileStart     = "file_start"
	TypeFileChunk     = "file_chunk"
	TypeFileEnd       = "file_end"
)

// Message is the envelope for every wire message.
type Message struct {
	V       int         `json:"v"`
	Seq     int64       `json:"seq"`
	Type    string      `json:"type"`
	Ts      int64       `json:"ts"`
	Payload interface{} `json:"payload"`
}

// HelloPayload is exchanged on first connect.
type HelloPayload struct {
	DeviceID           string `json:"device_id"`
	DisplayID          string `json:"display_id"`
	Name               string `json:"name"`
	ProtocolVersion    int    `json:"protocol_version"`
	SupportsRemembered bool   `json:"supports_remembered"`
}

// PairChallengePayload is sent by server to client to initiate PIN pairing.
type PairChallengePayload struct {
	PairingID  string `json:"pairing_id"`
	ServerName string `json:"server_name"`
}

// PairConfirmPayload carries either the client PIN or a receiver approval.
type PairConfirmPayload struct {
	PairingID string `json:"pairing_id"`
	PIN       string `json:"pin"`
	Trusted   bool   `json:"trusted,omitempty"`
}

// PairAcceptPayload is sent by server on successful pairing.
type PairAcceptPayload struct {
	Remembered bool   `json:"remembered"`
	Trusted    bool   `json:"trusted,omitempty"`
	SecretID   string `json:"secret_id,omitempty"`
	PairSecret string `json:"pair_secret,omitempty"`
}

// RememberedChallengePayload asks the peer to prove it still holds the remembered secret.
type RememberedChallengePayload struct {
	SecretID string `json:"secret_id"`
	Nonce    string `json:"nonce"`
}

// RememberedProofPayload returns the signed proof for one remembered challenge.
type RememberedProofPayload struct {
	SecretID string `json:"secret_id"`
	Proof    string `json:"proof,omitempty"`
}

// PairRejectPayload is sent on final rejection.
type PairRejectPayload struct {
	Reason string `json:"reason"`
}

// PairRetryPayload notifies client of wrong PIN with remaining attempts.
type PairRetryPayload struct {
	PairingID         string `json:"pairing_id"`
	AttemptsRemaining int    `json:"attempts_remaining"`
}

// PingPayload is empty.
type PingPayload struct{}

// PongPayload echoes the sender's timestamp for RTT calculation.
type PongPayload struct {
	EchoTs int64 `json:"echo_ts"`
}

// MouseMovePayload carries relative mouse delta plus screen context.
type MouseMovePayload struct {
	DX      float64 `json:"dx"`
	DY      float64 `json:"dy"`
	Button  string  `json:"button,omitempty"`
	AbsX    float64 `json:"abs_x"`
	AbsY    float64 `json:"abs_y"`
	ScreenW int     `json:"screen_w"`
	ScreenH int     `json:"screen_h"`
	Edge    string  `json:"edge,omitempty"`
	EdgePct float64 `json:"edge_pct,omitempty"`
}

// MouseMoveAbsPayload carries an absolute cursor target plus optional drag button context.
type MouseMoveAbsPayload struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Button string  `json:"button,omitempty"`
}

// MouseButtonPayload carries button press/release.
type MouseButtonPayload struct {
	Button     string `json:"button"`
	Pressed    bool   `json:"pressed"`
	ClickCount int    `json:"click_count,omitempty"`
}

// KeyDownPayload carries a key press.
type KeyDownPayload struct {
	Code int `json:"code"`
	Mods int `json:"mods"`
}

// KeyUpPayload carries a key release.
type KeyUpPayload struct {
	Code int `json:"code"`
	Mods int `json:"mods"`
}

// TextPayload carries a unicode string for injection.
type TextPayload struct {
	Text string `json:"text"`
}

// ScrollPayload carries scroll deltas.
type ScrollPayload struct {
	DX float64 `json:"dx"`
	DY float64 `json:"dy"`
}

// SwitchRequestPayload requests control handoff.
type SwitchRequestPayload struct {
	RequestID string  `json:"request_id,omitempty"`
	Trigger   string  `json:"trigger"`
	Edge      string  `json:"edge,omitempty"`
	EntryPct  float64 `json:"entry_pct,omitempty"`
	Direction string  `json:"direction,omitempty"`
}

// SwitchAckPayload reports whether the receiver reserved control for a request.
// A grant is valid only when RequestID matches the sender's pending request.
type SwitchAckPayload struct {
	RequestID string `json:"request_id,omitempty"`
	Granted   bool   `json:"granted"`
	Released  bool   `json:"released,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

// SwitchBackPayload releases a previously granted peer-control lease.
type SwitchBackPayload struct {
	RequestID string `json:"request_id,omitempty"`
	LeaseID   string `json:"lease_id,omitempty"`
}

// ClipboardSyncPayload carries text clipboard content across a P2P session.
type ClipboardSyncPayload struct {
	Text            string          `json:"text,omitempty"`
	HTML            string          `json:"html,omitempty"`
	ImageBase64     string          `json:"image_base64,omitempty"`
	ImageMIMEType   string          `json:"image_mime_type,omitempty"`
	Files           []ClipboardFile `json:"files,omitempty"`
	PasteAfterWrite bool            `json:"paste_after_write,omitempty"`
	ContentSHA      string          `json:"content_sha"`
	SourceID        string          `json:"source_id,omitempty"`
}

// ClipboardFile refers to a file already transferred in the same P2P session.
type ClipboardFile struct {
	TransferID string `json:"transfer_id"`
	Name       string `json:"name"`
	Directory  bool   `json:"directory,omitempty"`
	SHA256     string `json:"sha256"`
}

// FileStartPayload initiates a chunked file transfer over a P2P session.
type FileStartPayload struct {
	TransferID string `json:"transfer_id"`
	FileName   string `json:"file_name"`
	TotalBytes int64  `json:"total_bytes"`
	SHA256     string `json:"sha256,omitempty"`
}

// FileChunkPayload carries one base64-encoded chunk of a file transfer.
type FileChunkPayload struct {
	TransferID string `json:"transfer_id"`
	Offset     int64  `json:"offset"`
	DataBase64 string `json:"data_base64"`
}

// FileEndPayload finalizes a file transfer and carries its expected SHA-256 digest.
type FileEndPayload struct {
	TransferID string `json:"transfer_id"`
	SHA256     string `json:"sha256"`
	Canceled   bool   `json:"canceled,omitempty"`
}
