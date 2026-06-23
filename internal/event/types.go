package event

// P2P wire message types (v1.5.2-final-mvp protocol).
const (
	// Connection phase
	TypeHello = "hello"

	// Pairing phase
	TypePairChallenge = "pair_challenge"
	TypePairConfirm   = "pair_confirm"
	TypePairAccept    = "pair_accept"
	TypePairReject    = "pair_reject"
	TypePairRetry     = "pair_retry"

	// Session phase
	TypePing          = "ping"
	TypePong          = "pong"
	TypeMouseMove     = "mouse_move"
	TypeMouseButton   = "mouse_button"
	TypeKeyDown       = "key_down"
	TypeKeyUp         = "key_up"
	TypeScroll        = "scroll"
	TypeSwitchRequest = "switch_request"
	TypeSwitchAck     = "switch_ack"
	TypeSwitchBack    = "switch_back"
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

// PairConfirmPayload carries the PIN entered by the client (host).
type PairConfirmPayload struct {
	PairingID string `json:"pairing_id"`
	PIN       string `json:"pin"`
}

// PairAcceptPayload is sent by server on successful pairing.
type PairAcceptPayload struct {
	Remembered bool   `json:"remembered"`
	SecretID   string `json:"secret_id,omitempty"`
	PairSecret string `json:"pair_secret,omitempty"`
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

// MouseButtonPayload carries button press/release.
type MouseButtonPayload struct {
	Button  string `json:"button"`
	Pressed bool   `json:"pressed"`
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

// ScrollPayload carries scroll deltas.
type ScrollPayload struct {
	DX float64 `json:"dx"`
	DY float64 `json:"dy"`
}

// SwitchRequestPayload requests control handoff.
type SwitchRequestPayload struct {
	Trigger   string  `json:"trigger"`
	Edge      string  `json:"edge,omitempty"`
	EntryPct  float64 `json:"entry_pct,omitempty"`
	Direction string  `json:"direction,omitempty"`
}
