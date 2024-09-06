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

const infTTL = 1 * time.Hour // inf TTL for our test purposes.

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
	ctx := context.Background()
	reds, err := redis.Run(ctx, "redis:latest")
	if err != nil {
		return nil, err
	}
	addr, err := reds.Endpoint(ctx, "")
	if err != nil {
		return nil, err
	}
	redisAddr = addr
	log.Println(redisAddr)
	return reds.Terminate, nil
}

func initLimiter(t *testing.T, rds rediser, key string, limit Limit) *Limiter {
	t.Helper()
	l := NewLimiter(rds, key, limit)

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	if err := l.Reset(ctx); err != nil {
		t.Fatalf("Failed to reset limiter: %v", err)
	}
	return l
}

func testAllow(t *testing.T, l *Limiter, allows ...allow) {
	t.Helper()

	for i, allow := range allows {
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		status, err := l.allowAt(ctx, allow.now, 2*l.lim.Interval)
		cancel()

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
		log.Fatalf("Failed to setup Redis: %v", err)
		return 1
	}
	defer func() {
		if err := teardown(context.Background()); err != nil {
			log.Fatalf("Failed to setup Redis: %v", err)
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

		l.SetLimit(Limit{0, 3 * time.Second})
		testAllow(t, l, allow)

		l.SetLimit(Limit{0, 10 * time.Second})
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

	t.Run("sub-second", func(t *testing.T) {
		t.Parallel()

		l := initLimiter(t, rds, fmt.Sprintf("%s:%s", key, t.Name()), Limit{1, 2 * time.Millisecond})

		testAllow(t, l,
			allow{now, Status{false, 0, 2 * time.Millisecond}},
			allow{now.Add(1 * time.Millisecond), Status{true, 0, 1 * time.Millisecond}},
			allow{now.Add(2 * time.Millisecond), Status{false, 0, 2 * time.Millisecond}},
		)
	})

	t.Run("sub-millisecond", func(t *testing.T) {
		t.Parallel()

		l := initLimiter(t, rds, fmt.Sprintf("%s:%s", key, t.Name()), Limit{1, 500 * time.Nanosecond})

		testAllow(t, l,
			allow{now, Status{false, 0, 0 * time.Second}},
			allow{now.Add(1 * time.Millisecond), Status{false, 0, 0 * time.Second}},
		)

		l.SetLimit(Limit{3, 500 * time.Nanosecond})
		testAllow(t, l,
			allow{now, Status{false, 2, 0 * time.Second}},
			allow{now.Add(1 * time.Millisecond), Status{false, 2, 0 * time.Second}},
		)

		l.SetLimit(Limit{3, 500 * time.Nanosecond})
		testAllow(t, l,
			allow{now.Add(2 * time.Second), Status{false, 2, 0 * time.Second}},
			allow{now.Add(3 * time.Second), Status{false, 2, 0 * time.Second}},
		)
	})
}

func TestSetLimit(t *testing.T) {
	rds := rediser{rds.NewClient(&rds.Options{Addr: redisAddr})}
	key := "ratelimit:test_set_limit"

	t.Run("incr events", func(t *testing.T) {
		t.Parallel()

		l := initLimiter(t, rds, fmt.Sprintf("%s:%s", key, t.Name()), Limit{})

		lim := Limit{1, 10 * time.Second}
		l.SetLimit(lim)
		testAllow(t, l,
			allow{now, Status{false, 0, 10 * time.Second}},
			allow{now.Add(1 * time.Second), Status{true, 0, 9 * time.Second}},
		)

		lim.Events = 5
		l.SetLimit(lim)
		testAllow(t, l,
			allow{now.Add(2 * time.Second), Status{false, 3, 0 * time.Second}},
			allow{now.Add(3 * time.Second), Status{false, 2, 0 * time.Second}},
		)
	})

	t.Run("incr interval", func(t *testing.T) {
		t.Parallel()

		l := initLimiter(t, rds, fmt.Sprintf("%s:%s", key, t.Name()), Limit{})

		lim := Limit{1, 10 * time.Second}

		l.SetLimit(lim)
		testAllow(t, l, allow{now, Status{false, 0, 10 * time.Second}})
		testAllow(t, l, allow{now.Add(1 * time.Second), Status{true, 0, 9 * time.Second}})

		lim.Interval = 20 * time.Second
		l.SetLimit(lim)
		testAllow(t, l, allow{now.Add(2 * time.Second), Status{true, 0, 18 * time.Second}})
		testAllow(t, l, allow{now.Add(3 * time.Second), Status{true, 0, 17 * time.Second}})
	})

	t.Run("decr events", func(t *testing.T) {
		t.Parallel()

		l := initLimiter(t, rds, fmt.Sprintf("%s:%s", key, t.Name()), Limit{})

		lim := Limit{10, 10 * time.Second}
		l.SetLimit(lim)
		testAllow(t, l, allow{now, Status{false, 9, 0 * time.Second}})
		testAllow(t, l, allow{now.Add(1 * time.Second), Status{false, 8, 0 * time.Second}})
		testAllow(t, l, allow{now.Add(2 * time.Second), Status{false, 7, 0 * time.Second}})
		testAllow(t, l, allow{now.Add(3 * time.Second), Status{false, 6, 0 * time.Second}})
		testAllow(t, l, allow{now.Add(4 * time.Second), Status{false, 5, 0 * time.Second}})

		lim.Events = 2
		l.SetLimit(lim)
		testAllow(t, l, allow{now.Add(5 * time.Second), Status{true, 0, 8 * time.Second}})
		testAllow(t, l, allow{now.Add(6 * time.Second), Status{true, 0, 7 * time.Second}})
		testAllow(t, l, allow{now.Add(7 * time.Second), Status{true, 0, 6 * time.Second}})
	})

	t.Run("decr interval", func(t *testing.T) {
		t.Parallel()

		l := initLimiter(t, rds, fmt.Sprintf("%s:%s", key, t.Name()), Limit{})

		lim := Limit{4, 20 * time.Second}
		l.SetLimit(lim)
		testAllow(t, l, allow{now, Status{false, 3, 0 * time.Second}})
		testAllow(t, l, allow{now.Add(1 * time.Second), Status{false, 2, 0 * time.Second}})

		lim.Interval = 10 * time.Second
		l.SetLimit(lim)
		testAllow(t, l, allow{now.Add(2 * time.Second), Status{false, 1, 0 * time.Second}})
		testAllow(t, l, allow{now.Add(3 * time.Second), Status{false, 0, 7 * time.Second}})
		testAllow(t, l, allow{now.Add(4 * time.Second), Status{true, 0, 6 * time.Second}})
		testAllow(t, l, allow{now.Add(5 * time.Second), Status{true, 0, 5 * time.Second}})
	})
}

func TestInvalidInterval(t *testing.T) {
	rds := rediser{rds.NewClient(&rds.Options{Addr: redisAddr})}
	l := initLimiter(t, rds, "ratelimit:test_invalid_interval", Limit{1, 0})

	testAllowErr := func() {
		t.Helper()
		if _, err := l.allowAt(context.Background(), now, infTTL); !errors.Is(err, errInvalidInterval) {
			t.Errorf("Allow() returned error: %v, want %v", err, errInvalidInterval)
		}
	}

	testAllowErr()

	l.SetLimit(Limit{Interval: 0})
	testAllowErr()

	l.SetLimit(Limit{Interval: -1})
	testAllowErr()
}
