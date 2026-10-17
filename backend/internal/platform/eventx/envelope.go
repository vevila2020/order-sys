package eventx

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

type Envelope struct {
	EventID     string          `json:"event_id"`
	EventType   string          `json:"event_type"`
	AggregateID string          `json:"aggregate_id"`
	OccurredAt  time.Time       `json:"occurred_at"`
	Payload     json.RawMessage `json:"payload"`
}

func New(eventType, aggregateID string, payload []byte, occurredAt time.Time) (Envelope, error) {
	if !json.Valid(payload) {
		return Envelope{}, json.Unmarshal(payload, &struct{}{})
	}
	return Envelope{
		EventID:     uuid.NewString(),
		EventType:   eventType,
		AggregateID: aggregateID,
		OccurredAt:  occurredAt.UTC(),
		Payload:     json.RawMessage(payload),
	}, nil
}
