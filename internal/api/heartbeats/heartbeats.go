// Package heartbeats keeps the last two VM lists each host reported. The
// reconciler compares them with the database; a difference has to show up in
// both before it is acted on (spec section 10.7).
package heartbeats

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"zynthera/internal/shared/vmtypes"
)

// Record is one heartbeat's VM list and when it arrived.
type Record struct {
	ReceivedAt time.Time          `json:"received_at"`
	VMs        []vmtypes.VMStatus `json:"vms"`
}

// StatusOf returns the reported status of a VM, or "missing" when the host
// did not list it.
func (r Record) StatusOf(vmID string) string {
	for _, vm := range r.VMs {
		if vm.ID == vmID {
			return vm.Status
		}
	}
	return vmtypes.StatusMissing
}

func key(hostID uuid.UUID) string { return "zyn:heartbeats:" + hostID.String() }

// How long records live without a newer heartbeat. An offline host's old
// reports must not be acted on.
const ttl = 2 * time.Minute

// Save stores a heartbeat's VM list as the newest record of the host and
// drops everything but the newest two.
func Save(ctx context.Context, rdb *redis.Client, hostID uuid.UUID, rec Record) error {
	raw, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("heartbeats: encode: %w", err)
	}
	pipe := rdb.TxPipeline()
	pipe.LPush(ctx, key(hostID), raw)
	pipe.LTrim(ctx, key(hostID), 0, 1)
	pipe.Expire(ctx, key(hostID), ttl)
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("heartbeats: save: %w", err)
	}
	return nil
}

// LastTwo returns up to two records, newest first.
func LastTwo(ctx context.Context, rdb *redis.Client, hostID uuid.UUID) ([]Record, error) {
	raws, err := rdb.LRange(ctx, key(hostID), 0, 1).Result()
	if err != nil {
		return nil, fmt.Errorf("heartbeats: load: %w", err)
	}
	out := make([]Record, 0, len(raws))
	for _, raw := range raws {
		var rec Record
		if err := json.Unmarshal([]byte(raw), &rec); err != nil {
			return nil, fmt.Errorf("heartbeats: decode: %w", err)
		}
		out = append(out, rec)
	}
	return out, nil
}
