package store

import (
	"encoding/json"
	"time"
)

// Envelope is the on-disk framing of a journal event.
type Envelope struct {
	Seq     int64           `json:"seq"`
	Type    string          `json:"type"`
	At      time.Time       `json:"at"`
	IdemKey string          `json:"idem_key,omitempty"`
	Body    json.RawMessage `json:"body"`
}

// Frame serialises one event for the journal.
func Frame(seq int64, typ string, at time.Time, idemKey string, body any) ([]byte, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	return json.Marshal(Envelope{Seq: seq, Type: typ, At: at, IdemKey: idemKey, Body: raw})
}

func eventType(data []byte) string {
	var env Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return ""
	}
	return env.Type
}
