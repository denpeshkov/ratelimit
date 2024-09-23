package throttle

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"testing"
	"time"

	rds "github.com/redis/go-redis/v9"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/redis"
)

var (
	redisAddr string
	now       = time.Now()
)

type rediser struct {
	rds *rds.Client
}

func (r rediser) ScriptLoad(ctx context.Context, script string) (string, error) {
	return r.rds.ScriptLoad(ctx, script).Result()
}
func (r rediser) EvalSHA(ctx context.Context, sha1 string, keys []string, args ...any) (any, error) {
	return r.rds.EvalSha(ctx, sha1, keys, args...).Result()
}
func (r rediser) Del(ctx context.Context, keys ...string) (int64, error) {
	return r.rds.Del(ctx, keys...).Result()
}

type allow struct {
	now    time.Time
	status *Status
}

type noopLogger struct{}

func (noopLogger) Printf(format string, v ...interface{}) {}

func setupRedis() (teardown func(ctx context.Context) error, err error) {
	reds, err := redis.Run(context.Background(), "redis:latest", testcontainers.WithLogger(noopLogger{}))
	if err != nil {
		return nil, err
	}
	addr, err := reds.Endpoint(context.Background(), "")
	if err != nil {
		return nil, err
	}
	redisAddr = addr
	return reds.Terminate, nil
}

func initLimiter(t *testing.T, rds rediser, key string, limit Limit) *Limiter {
	t.Helper()

	l, err := NewLimiter(rds, key, limit)
	if err != nil {
		t.Fatalf("Failed to create a limiter: %v", err)
	}
	if err := l.Reset(context.Background()); err != nil {
		t.Fatalf("Failed to reset a limiter: %v", err)
	}
	return l
}

func testAllow(t *testing.T, l *Limiter, allows ...allow) {
	t.Helper()

	for _, a := range allows {
		status, err := l.allowAt(context.Background(), a.now, Inf)
		if err != nil {
			t.Errorf("allowAt(%s) unexpected error: %v", a.now.Format("15:04:05.000"), err)
		}
		if *status != *a.status {
			t.Errorf("allowAt(%s) = %s, want %s", a.now.Format("15:04:05.000"), status, a.status)
		}
	}
}

func TestMain(m *testing.M) {
	os.Exit(testMain(m))
}
func testMain(m *testing.M) (code int) {
	teardown, err := setupRedis()
	if err != nil {
		log.Printf("Failed to setup Redis: %v\n", err)
		return 1
	}
	defer func() {
		if err := teardown(context.Background()); err != nil {
			log.Printf("Failed to teardown Redis: %v\n", err)
			code = 1
		}
	}()
	return m.Run()
}

const delta = 1 * time.Millisecond // Limiter resolution.s

func TestAllow(t *testing.T) {
	rds := rediser{rds.NewClient(&rds.Options{Addr: redisAddr})}
	key := "ratelimit:test_allow"

	t.Run("disallow all", func(t *testing.T) {
		t.Parallel()

		allow := allow{now, &Status{true, 0, Inf}}

		l := initLimiter(t, rds, fmt.Sprintf("%s:%s", key, t.Name()), Limit{Events: 0, Interval: 1 * time.Second})
		testAllow(t, l, allow)
	})

	t.Run("basic", func(t *testing.T) {
		t.Parallel()

		lims := []Limit{
			{Events: 1, Interval: 10 * time.Millisecond},
			{Events: 2, Interval: 10 * time.Millisecond},
			{Events: 10, Interval: 10 * time.Millisecond},
			{Events: 1, Interval: 999 * time.Millisecond},
			{Events: 2, Interval: 999 * time.Millisecond},
			{Events: 10, Interval: 999 * time.Millisecond},
			{Events: 1, Interval: 1 * time.Second},
			{Events: 2, Interval: 1 * time.Second},
			{Events: 10, Interval: 1 * time.Second},
		}

		for _, lim := range lims {
			t.Run(lim.String(), func(t *testing.T) {
				t.Parallel()

				now := now
				t.Logf("now: %s", now.Format("15:04:05.000"))

				l := initLimiter(t, rds, fmt.Sprintf("%s:%s", key, t.Name()), lim)

				// Hit until last unlimited event.
				want := Status{Limited: false, Remaining: lim.Events - 1, Delay: 0}
				for want.Remaining > 0 {
					s, err := l.allowAt(context.Background(), now, Inf)
					if err != nil {
						t.Fatalf("allowAt(%s) error: %v", now.Format("15:04:05.000"), err)
					}
					if *s != want {
						t.Errorf("allowAt(%s) = %s, want %s", now.Format("15:04:05.000"), s, want)
					}
					want.Remaining--
					now = now.Add(delta)
				}

				// Last unlimited event should have some positive delay.
				s, err := l.allowAt(context.Background(), now, Inf)
				if err != nil {
					t.Fatalf("allowAt(%s) error: %v", now.Format("15:04:05.000"), err)
				}
				if s.Limited || s.Delay <= 0 || s.Remaining > 0 {
					t.Errorf("allowAt(%s) = %s, want (unlimited, 0 req, positive delay)", now.Format("15:04:05.000"), s)
				}

				// These events should be limited.
				stopTime := now.Add(s.Delay)
				now = now.Add(delta)
				want = Status{Limited: true, Remaining: 0, Delay: s.Delay - delta}
				for now.Before(stopTime) {
					s, err := l.allowAt(context.Background(), now, Inf)
					if err != nil {
						t.Fatalf("allowAt(%s) error: %v", now.Format("15:04:05.000"), err)
					}
					if *s != want {
						t.Errorf("allowAt(%s) = %s, want %s", now.Format("15:04:05.000"), s, want)
					}
					want.Delay -= delta
					now = now.Add(delta)
				}
			})
		}
	})

	t.Run("delta exceeds interval", func(t *testing.T) {
		t.Parallel()

		lims := []Limit{
			{Events: 1, Interval: 1 * time.Millisecond},
			{Events: 2, Interval: 1 * time.Millisecond},
			{Events: 1, Interval: 1 * time.Second},
			{Events: 2, Interval: 1 * time.Second},
		}

		for _, lim := range lims {
			t.Run(lim.String(), func(t *testing.T) {
				t.Parallel()

				now := now
				t.Logf("now: %s", now.Format("15:04:05.000"))

				l := initLimiter(t, rds, fmt.Sprintf("%s:%s", key, t.Name()), lim)

				// First hit.
				s1, err := l.allowAt(context.Background(), now, Inf)
				if err != nil {
					t.Fatalf("allowAt(%s) error: %v", now.Format("15:04:05.000"), err)
				}

				// Second hit. Limit should be reset.
				now = now.Add(lim.Interval + 1*time.Millisecond)
				s2, err := l.allowAt(context.Background(), now, Inf)
				if err != nil {
					t.Fatalf("allowAt(%s) error: %v", now.Format("15:04:05.000"), err)
				}
				if *s1 != *s2 {
					t.Errorf("Limit not reset. allowAt(%s) = %s, want %s", now.Format("15:04:05.000"), s2, s1)
				}
			})
		}
	})
}

func TestSetLimit(t *testing.T) {
	rds := rediser{rds.NewClient(&rds.Options{Addr: redisAddr})}
	key := "ratelimit:test_set_limit"

	lim := Limit{Events: 1, Interval: 1 * time.Second}
	l := initLimiter(t, rds, fmt.Sprintf("%s:%s", key, t.Name()), lim)

	if err := l.SetLimit(context.Background(), lim); err != nil {
		t.Fatalf("SetLimit(%s) error: %v", lim, err)
	}
	if l.Limit() != lim {
		t.Errorf("SetLimit(%s) failed to set limit; Limit() = %v, want %v", lim, l.Limit(), lim)
	}

	lim = Limit{Events: 5, Interval: 10 * time.Second}
	if err := l.SetLimit(context.Background(), lim); err != nil {
		t.Fatalf("SetLimit(%s) error: %v", lim, err)
	}
	if l.Limit() != lim {
		t.Errorf("SetLimit(%s) failed to set limit; Limit() = %v, want %v", lim, l.Limit(), lim)
	}
}

func TestInvalidLimit(t *testing.T) {

	t.Run("invalid interval", func(t *testing.T) {
		intervals := []time.Duration{-1, 0, 999 * time.Nanosecond}
		for _, in := range intervals {
			lim := Limit{Events: 1, Interval: in}
			l, err := NewLimiter(nil, "", lim)
			if !errors.Is(err, errInvalidInterval) {
				t.Fatalf("NewLimiter(%s) error: %v, want: %v", lim, err, errInvalidInterval)
			}
			if err := l.SetLimit(context.Background(), lim); !errors.Is(err, errInvalidInterval) {
				t.Fatalf("SetLimit(%s) error: %v, want: %v", lim, err, errInvalidInterval)
			}
		}
	})

	t.Run("invalid events", func(t *testing.T) {
		lim := Limit{Events: -1, Interval: 1 * time.Second}
		l, err := NewLimiter(nil, "", lim)

		if !errors.Is(err, errInvalidEvents) {
			t.Fatalf("NewLimiter(%s) error: %v, want: %v", lim, err, errInvalidEvents)
		}
		if err := l.SetLimit(context.Background(), lim); !errors.Is(err, errInvalidEvents) {
			t.Fatalf("SetLimit(%s) error: %v, want: %v", lim, err, errInvalidEvents)
		}
	})
}

func TestReset(t *testing.T) {
	t.Parallel()

	rds := rediser{rds.NewClient(&rds.Options{Addr: redisAddr})}
	key := "ratelimit:test_reset"

	tests := []struct {
		name   string
		events int
	}{
		{"limited", 1},
		{"unlimited", 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			now := now
			l := initLimiter(t, rds, fmt.Sprintf("%s:%s", key, t.Name()), Limit{Events: 1, Interval: 1 * time.Second})

			// First event.
			s1, err := l.allowAt(context.Background(), now, Inf)
			if err != nil {
				t.Fatalf("allowAt(%s) error: %v", now.Format("15:04:05.000"), err)
			}

			now = now.Add(delta)

			// After Reset() second event should return the same status as the first event.
			if err := l.Reset(context.Background()); err != nil {
				t.Fatalf("Reset() error: %v", err)
			}
			s2, err := l.allowAt(context.Background(), now, Inf)
			if err != nil {
				t.Fatalf("allowAt(%s) error: %v", now.Format("15:04:05.000"), err)
			}
			if *s1 != *s2 {
				t.Errorf("Limit not reset. allowAt(%s) = %s, want %s", now.Format("15:04:05.000"), s2, s1)
			}
		})
	}
}
