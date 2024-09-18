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
	status Status
}

func setupRedis() (teardown func(ctx context.Context) error, err error) {
	reds, err := redis.Run(context.Background(), "redis:latest")
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

	for i, allow := range allows {
		status, err := l.allowAt(context.Background(), allow.now, 2*l.lim.Interval)
		if err != nil {
			t.Fatalf("Step %d: Allow() unexpected error: %v", i, err)
		}
		if *status != allow.status {
			t.Errorf("Step %d: Allow() = %s, want %s", i, status, allow.status)
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
			log.Printf("Failed to setup Redis: %v\n", err)
			code = 1
		}
	}()
	return m.Run()
}

func TestAllow(t *testing.T) {
	rds := rediser{rds.NewClient(&rds.Options{Addr: redisAddr})}
	key := "ratelimit:test_allow"

	t.Run("disallow all", func(t *testing.T) {
		t.Parallel()

		l := initLimiter(t, rds, fmt.Sprintf("%s:%s", key, t.Name()), Limit{0, 1 * time.Second})
		allow := allow{now, Status{true, 0, Inf}}

		testAllow(t, l, allow)

		if err := l.SetLimit(context.Background(), Limit{0, 3 * time.Second}); err != nil {
			t.Fatalf("SetLimit() unexpected error: %v", err)
		}
		testAllow(t, l, allow)

		if err := l.SetLimit(context.Background(), Limit{0, 10 * time.Second}); err != nil {
			t.Fatalf("SetLimit() unexpected error: %v", err)
		}
		testAllow(t, l, allow)
	})

	t.Run("basic", func(t *testing.T) {
		t.Parallel()

		l := initLimiter(t, rds, fmt.Sprintf("%s:%s", key, t.Name()), Limit{3, 5 * time.Second})

		testAllow(t, l,
			allow{now, Status{false, 2, 0 * time.Second}},
			allow{now.Add(1 * time.Second), Status{false, 1, 0 * time.Second}},
			allow{now.Add(2 * time.Second), Status{false, 0, 3 * time.Second}},
			allow{now.Add(3 * time.Second), Status{true, 0, 2 * time.Second}},
			allow{now.Add(4 * time.Second), Status{true, 0, 1 * time.Second}},
			allow{now.Add(5 * time.Second), Status{false, 0, 1 * time.Second}},
			allow{now.Add(6 * time.Second), Status{false, 0, 1 * time.Second}},
			allow{now.Add(7 * time.Second), Status{false, 0, 3 * time.Second}},
			allow{now.Add(8 * time.Second), Status{true, 0, 2 * time.Second}},
		)
	})

	t.Run("once per window", func(t *testing.T) {
		t.Parallel()

		l := initLimiter(t, rds, fmt.Sprintf("%s:%s", key, t.Name()), Limit{5, 3 * time.Second})

		testAllow(t, l,
			allow{now, Status{false, 4, 0 * time.Second}},
			allow{now.Add(10 * time.Second), Status{false, 4, 0 * time.Second}},
			allow{now.Add(20 * time.Second), Status{false, 4, 0 * time.Second}},
		)
	})

	t.Run("big window", func(t *testing.T) {
		t.Parallel()

		l := initLimiter(t, rds, fmt.Sprintf("%s:%s", key, t.Name()), Limit{1, 100 * time.Second})

		testAllow(t, l,
			allow{now, Status{false, 0, 100 * time.Second}},
			allow{now.Add(5 * time.Second), Status{true, 0, 95 * time.Second}},
			allow{now.Add(50 * time.Second), Status{true, 0, 50 * time.Second}},
			allow{now.Add(99 * time.Second), Status{true, 0, 1 * time.Second}},
			allow{now.Add(100 * time.Second), Status{false, 0, 100 * time.Second}},
			allow{now.Add(199 * time.Second), Status{true, 0, 1 * time.Second}},
			allow{now.Add(201 * time.Second), Status{false, 0, 100 * time.Second}},
		)
	})

	t.Run("window larger now", func(t *testing.T) {
		t.Parallel()

		limi := time.Duration(now.Add(1*time.Hour).UnixMilli()) * time.Millisecond // interval larger than now
		l := initLimiter(t, rds, fmt.Sprintf("%s:%s", key, t.Name()), Limit{2, limi})

		testAllow(t, l,
			allow{now, Status{false, 1, 0}},
			allow{now.Add(1 * time.Second), Status{false, 0, limi - 1*time.Second}},
			allow{now.Add(10 * time.Second), Status{true, 0, limi - 10*time.Second}},
		)
	})

	t.Run("sub-second", func(t *testing.T) {
		t.Parallel()

		l := initLimiter(t, rds, fmt.Sprintf("%s:%s", key, t.Name()), Limit{1, 2 * time.Millisecond})

		testAllow(t, l,
			allow{now, Status{false, 0, 2 * time.Millisecond}},
			allow{now.Add(1 * time.Millisecond), Status{true, 0, 1 * time.Millisecond}},
			allow{now.Add(2 * time.Millisecond), Status{false, 0, 2 * time.Millisecond}},
		)
	})
}

func TestSetLimit(t *testing.T) {
	rds := rediser{rds.NewClient(&rds.Options{Addr: redisAddr})}
	key := "ratelimit:test_set_limit"

	t.Run("incr events", func(t *testing.T) {
		t.Parallel()

		lim := Limit{1, 10 * time.Second}

		l := initLimiter(t, rds, fmt.Sprintf("%s:%s", key, t.Name()), lim)
		testAllow(t, l,
			allow{now, Status{false, 0, 10 * time.Second}},
			allow{now.Add(1 * time.Second), Status{true, 0, 9 * time.Second}},
		)

		lim.Events = 5
		if err := l.SetLimit(context.Background(), lim); err != nil {
			t.Fatalf("SetLimit() unexpected error: %v", err)
		}
		testAllow(t, l,
			allow{now.Add(2 * time.Second), Status{false, 3, 0 * time.Second}},
			allow{now.Add(3 * time.Second), Status{false, 2, 0 * time.Second}},
		)
	})

	t.Run("incr interval", func(t *testing.T) {
		t.Parallel()

		lim := Limit{1, 10 * time.Second}
		l := initLimiter(t, rds, fmt.Sprintf("%s:%s", key, t.Name()), lim)
		testAllow(t, l, allow{now, Status{false, 0, 10 * time.Second}})
		testAllow(t, l, allow{now.Add(1 * time.Second), Status{true, 0, 9 * time.Second}})

		lim.Interval = 20 * time.Second
		if err := l.SetLimit(context.Background(), lim); err != nil {
			t.Fatalf("SetLimit() unexpected error: %v", err)
		}
		testAllow(t, l, allow{now.Add(2 * time.Second), Status{true, 0, 18 * time.Second}})
		testAllow(t, l, allow{now.Add(3 * time.Second), Status{true, 0, 17 * time.Second}})
	})

	t.Run("decr events", func(t *testing.T) {
		t.Parallel()

		lim := Limit{10, 10 * time.Second}

		l := initLimiter(t, rds, fmt.Sprintf("%s:%s", key, t.Name()), lim)
		testAllow(t, l, allow{now, Status{false, 9, 0 * time.Second}})
		testAllow(t, l, allow{now.Add(1 * time.Second), Status{false, 8, 0 * time.Second}})
		testAllow(t, l, allow{now.Add(2 * time.Second), Status{false, 7, 0 * time.Second}})
		testAllow(t, l, allow{now.Add(3 * time.Second), Status{false, 6, 0 * time.Second}})
		testAllow(t, l, allow{now.Add(4 * time.Second), Status{false, 5, 0 * time.Second}})

		lim.Events = 2
		if err := l.SetLimit(context.Background(), lim); err != nil {
			t.Fatalf("SetLimit() unexpected error: %v", err)
		}
		testAllow(t, l, allow{now.Add(5 * time.Second), Status{true, 0, 8 * time.Second}})
		testAllow(t, l, allow{now.Add(6 * time.Second), Status{true, 0, 7 * time.Second}})
		testAllow(t, l, allow{now.Add(7 * time.Second), Status{true, 0, 6 * time.Second}})
	})

	t.Run("decr interval", func(t *testing.T) {
		t.Parallel()

		lim := Limit{4, 20 * time.Second}

		l := initLimiter(t, rds, fmt.Sprintf("%s:%s", key, t.Name()), lim)
		testAllow(t, l, allow{now, Status{false, 3, 0 * time.Second}})
		testAllow(t, l, allow{now.Add(1 * time.Second), Status{false, 2, 0 * time.Second}})

		lim.Interval = 10 * time.Second
		if err := l.SetLimit(context.Background(), lim); err != nil {
			t.Fatalf("SetLimit() unexpected error: %v", err)
		}
		testAllow(t, l, allow{now.Add(2 * time.Second), Status{false, 1, 0 * time.Second}})
		testAllow(t, l, allow{now.Add(3 * time.Second), Status{false, 0, 7 * time.Second}})
		testAllow(t, l, allow{now.Add(4 * time.Second), Status{true, 0, 6 * time.Second}})
		testAllow(t, l, allow{now.Add(5 * time.Second), Status{true, 0, 5 * time.Second}})
	})
}

func TestInvalidInterval(t *testing.T) {
	tests := []Limit{
		{1, -1},
		{1, 0},
		{1, 500 * time.Nanosecond},
	}

	for _, tt := range tests {
		if _, err := NewLimiter(nil, "", tt); !errors.Is(err, errInvalidInterval) {
			t.Fatalf("NewSlidingLog() unexpected error: %v", err)
		}
	}
}

func TestReset(t *testing.T) {
	t.Parallel()

	rds := rediser{rds.NewClient(&rds.Options{Addr: redisAddr})}
	key := "ratelimit:test_reset"

	l := initLimiter(t, rds, fmt.Sprintf("%s:%s", key, t.Name()), Limit{4, 20 * time.Second})
	testAllow(t, l,
		allow{now, Status{false, 3, 0 * time.Second}},
		allow{now.Add(1 * time.Second), Status{false, 2, 0 * time.Second}},
		allow{now.Add(2 * time.Second), Status{false, 1, 0 * time.Second}},
		allow{now.Add(3 * time.Second), Status{false, 0, 17 * time.Second}},
		allow{now.Add(4 * time.Second), Status{true, 0, 16 * time.Second}},
	)
	if err := l.Reset(context.Background()); err != nil {
		t.Fatalf("Reset() returned an error: %v", err)
	}
	testAllow(t, l,
		allow{now, Status{false, 3, 0 * time.Second}},
		allow{now.Add(1 * time.Second), Status{false, 2, 0 * time.Second}},
		allow{now.Add(2 * time.Second), Status{false, 1, 0 * time.Second}},
		allow{now.Add(3 * time.Second), Status{false, 0, 17 * time.Second}},
		allow{now.Add(4 * time.Second), Status{true, 0, 16 * time.Second}},
	)
}
