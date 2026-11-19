// Package events carries live VM updates from the workers to the browsers.
// Workers publish every VM change to a Redis channel named for the owning
// user; the Server-Sent Events endpoint subscribes to that channel.
package events

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// Bus publishes and subscribes to per-user VM updates.
type Bus struct {
	rdb *redis.Client
}

// NewBus returns a Bus on rdb.
func NewBus(rdb *redis.Client) *Bus { return &Bus{rdb: rdb} }

func channel(userID uuid.UUID) string { return "zyn:vm-events:" + userID.String() }

// Publish sends one VM object (already encoded as JSON) to the user's
// listeners. Nobody listening is not an error.
func (b *Bus) Publish(ctx context.Context, userID uuid.UUID, vmJSON []byte) error {
	if err := b.rdb.Publish(ctx, channel(userID), vmJSON).Err(); err != nil {
		return fmt.Errorf("publish vm event: %w", err)
	}
	return nil
}

// Subscribe returns a channel of VM objects for one user. It is confirmed
// subscribed before Subscribe returns, so nothing published afterwards is
// missed. Call stop when done.
func (b *Bus) Subscribe(ctx context.Context, userID uuid.UUID) (updates <-chan []byte, stop func(), err error) {
	sub := b.rdb.Subscribe(ctx, channel(userID))
	if _, err := sub.Receive(ctx); err != nil {
		_ = sub.Close()
		return nil, nil, fmt.Errorf("subscribe to vm events: %w", err)
	}
	out := make(chan []byte, 16)
	go func() {
		defer close(out)
		for msg := range sub.Channel() {
			select {
			case out <- []byte(msg.Payload):
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, func() { _ = sub.Close() }, nil
}
