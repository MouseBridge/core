package event

const (
	TypeHandshake     = "handshake"
	TypePairRequest   = "pair_request"
	TypePairPin       = "pair_pin"
	TypePairConfirm   = "pair_confirm"
	TypePairAccept    = "pair_accept"
	TypePairReject    = "pair_reject"
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

// HandshakePayload is exchanged on first connect.
type HandshakePayload struct {
	DeviceID string `json:"device_id"`
	Name     string `json:"name"`
	Platform string `json:"platform"`
}

// PairRequestPayload is sent by the host to initiate pairing.
type PairRequestPayload struct {
	DeviceID string `json:"device_id"`
	Name     string `json:"name"`
}

// PairPinPayload carries the PIN from slave to host.
type PairPinPayload struct {
	PIN string `json:"pin"`
}

// PairConfirmPayload carries the PIN entered by the host.
type PairConfirmPayload struct {
	PIN string `json:"pin"`
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

// TrustedDevice is persisted to trusted.json after pairing.
type TrustedDevice struct {
	DeviceID string `json:"device_id"`
	Name     string `json:"name"`
}
