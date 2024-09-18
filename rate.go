package throttle

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

// Inf is the infinite duration.
const Inf = time.Duration(math.MaxInt64)

var errInvalidInterval = errors.New("limit interval below 1 ms")

// Rediser defines an interface to abstract a Redis client.
type Rediser interface {
	// ScriptLoad preloads a Lua script into Redis and returns its SHA-1 hash.
	ScriptLoad(ctx context.Context, script string) (string, error)
	// EvalSHA executes a preloaded Lua script using its SHA-1 hash.
	EvalSHA(ctx context.Context, sha1 string, keys []string, args ...any) (any, error)
	// Del removes the specified keys. A key is ignored if it does not exist.
	Del(ctx context.Context, keys ...string) (int64, error)
}

// Limit defines the maximum number of events allowed within a specified time interval.
// Setting Events to zero disallows all events. Interval must be at least 1 millisecond.
type Limit struct {
	Events   int
	Interval time.Duration
}

func (l Limit) String() string {
	return fmt.Sprintf("%d req in %s", l.Events, l.Interval.String())
}

// Status represents the result of evaluating the rate limit.
type Status struct {
	// Limited indicates whether the current event was limited.
	Limited bool
	// Remaining specifies the number of events left in the current limit window.
	Remaining int
	// Delay is the duration until the next event is permitted.
	// A zero duration means the event can occur immediately.
	// An [Inf] duration indicates that no events are allowed.
	Delay time.Duration
}

func (s Status) String() string {
	ra := s.Delay.String()
	if s.Delay == Inf {
		ra = "Inf"
	}
	l := "unlimited"
	if s.Limited {
		l = "limited"
	}
	return fmt.Sprintf("(%s, %d req, %s)", l, s.Remaining, ra)
}

//go:embed allow.lua
var luaScript string

// A Limiter controls how frequently events are allowed to happen.
type Limiter struct {
	rds        Rediser
	scriptSHA1 string
	key        string
	lim        Limit
}

// NewLimiter returns a new [Limiter] for the given key that allows events up to the specified limit.
// Creating multiple [Limiter] instances for the same key with different limits may violate limits.
// It implements a "sliding window log" algorithm backed by [Redis].
//
// [Redis]: https://redis.io
func NewLimiter(rds Rediser, key string, limit Limit) (*Limiter, error) {
	if limit.Interval.Milliseconds() <= 0 {
		return nil, errInvalidInterval
	}
	return &Limiter{rds: rds, scriptSHA1: "", key: key, lim: limit}, nil
}

// Allow returns the result of evaluating whether the event can occur now.
func (l *Limiter) Allow(ctx context.Context) (*Status, error) {
	return l.allowAt(ctx, time.Now(), 2*l.lim.Interval)
}

func (l *Limiter) allowAt(ctx context.Context, now time.Time, keyTTL time.Duration) (*Status, error) {
	if l.lim.Events == 0 {
		return &Status{Limited: true, Remaining: 0, Delay: Inf}, nil
	}

	keys := []string{l.key}
	args := []any{l.lim.Events, l.lim.Interval.Milliseconds(), now.UnixMilli(), keyTTL.Milliseconds()}

	v, err := l.execScript(ctx, keys, args...)
	if err != nil {
		return nil, err
	}
	values := v.([]interface{})
	return &Status{
		Limited:   values[0].(int64) != 0,
		Remaining: int(values[1].(int64)),
		Delay:     time.Duration(values[2].(int64)) * time.Millisecond,
	}, nil
}

func (l *Limiter) execScript(ctx context.Context, keys []string, args ...any) (any, error) {
	v, err := l.rds.EvalSHA(ctx, l.scriptSHA1, keys, args...)
	if err != nil && strings.HasPrefix(err.Error(), "NOSCRIPT") {
		var sha1 string
		if sha1, err = l.rds.ScriptLoad(ctx, luaScript); err == nil {
			l.scriptSHA1 = sha1
			v, err = l.rds.EvalSHA(ctx, l.scriptSHA1, keys, args...)
		}
	}
	if err != nil {
		return nil, err
	}
	return v, nil
}

// Limit returns the current limit.
func (l *Limiter) Limit() Limit {
	return l.lim
}

// SetLimit sets a new [Limit] for the limiter.
// If the new limit's Interval exceeds the current one, the new limit may be
// temporarily violated by up to the difference between the new and current limit's Interval durations.
func (l *Limiter) SetLimit(_ context.Context, newLimit Limit) error {
	if newLimit.Interval.Milliseconds() <= 0 {
		return errInvalidInterval
	}
	l.lim = newLimit
	return nil
}

// Reset clears all limitations and previous usage of the limiter.
func (l *Limiter) Reset(ctx context.Context) error {
	_, err := l.rds.Del(ctx, l.key)
	return err
}
