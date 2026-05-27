package daemon

import (
	"time"

	"github.com/mousebridge/core/internal/event"
)

func buildPairConfirm(pairingID, pin string) event.Message {
	return event.Message{
		V:   1,
		Seq: time.Now().UnixMilli(),
		Type: event.TypePairConfirm,
		Ts:  time.Now().UnixMilli(),
		Payload: event.PairConfirmPayload{
			PairingID: pairingID,
			PIN:       pin,
		},
	}
}
