package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// Rule is one rate limit: at most Max events per Window.
type Rule struct {
	Name   string
	Max    int
	Window time.Duration
}

// The limits from spec section 10.2.
var (
	RuleSignupPerIP        = Rule{"signup_ip", 5, time.Hour}
	RuleLoginPerIPEmail    = Rule{"login_ip_email", 10, 15 * time.Minute}
	RuleForgotPerEmail     = Rule{"forgot_email", 3, time.Hour}
	RuleForgotPerIP        = Rule{"forgot_ip", 10, time.Hour}
	RuleResendVerification = Rule{"resend_verification_user", 3, time.Hour}
	RuleTokensPerUser      = Rule{"tokens_user", 30, time.Minute}
	RuleVMActionsPerUser   = Rule{"vm_actions_user", 20, time.Minute}
)

// Limiter counts events and says whether one more is allowed.
type Limiter interface {
	// Allow records one event for key under rule. When the limit is already
	// reached it returns false and how long to wait.
	Allow(ctx context.Context, rule Rule, key string) (allowed bool, retryAfter time.Duration, err error)
}

// RedisLimiter is a fixed-window counter stored in Redis.
type RedisLimiter struct {
	rdb *redis.Client
}

// NewRedisLimiter returns a Limiter backed by rdb.
func NewRedisLimiter(rdb *redis.Client) *RedisLimiter { return &RedisLimiter{rdb: rdb} }

// The counter and its expiry are set in one script so a crash between the two
// can never leave a counter that lives forever.
var rateScript = redis.NewScript(`
local n = redis.call('INCR', KEYS[1])
if n == 1 then
  redis.call('PEXPIRE', KEYS[1], ARGV[1])
end
local ttl = redis.call('PTTL', KEYS[1])
if ttl < 0 then
  redis.call('PEXPIRE', KEYS[1], ARGV[1])
  ttl = tonumber(ARGV[1])
end
return {n, ttl}
`)

// Allow implements Limiter.
func (l *RedisLimiter) Allow(ctx context.Context, rule Rule, key string) (bool, time.Duration, error) {
	// Keys can contain email addresses, so only a hash is stored.
	sum := sha256.Sum256([]byte(key))
	redisKey := "zyn:rl:" + rule.Name + ":" + hex.EncodeToString(sum[:16])

	res, err := rateScript.Run(ctx, l.rdb, []string{redisKey}, rule.Window.Milliseconds()).Int64Slice()
	if err != nil {
		return false, 0, fmt.Errorf("rate limit %s: %w", rule.Name, err)
	}
	if len(res) != 2 {
		return false, 0, fmt.Errorf("rate limit %s: unexpected script reply", rule.Name)
	}
	count, ttl := res[0], time.Duration(res[1])*time.Millisecond
	if count > int64(rule.Max) {
		return false, ttl, nil
	}
	return true, 0, nil
}
