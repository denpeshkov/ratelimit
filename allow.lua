-- key is a key to associate with this rate limiter
-- limit_events is the maximum number of events to be allowed in a limiting interval. It must be > 0
-- limit_interval is the duration of the limiting interval in milliseconds
-- now is the current Unix time in milliseconds
-- key_ttl is an optional timeout in milliseconds for the key. It must be >= than limit_interval
-- returns limited, remaining, delay
local key = KEYS[1]
local limit_events = tonumber(ARGV[1])
local limit_interval = tonumber(ARGV[2])
local now = tonumber(ARGV[3])
local key_ttl = tonumber(ARGV[4])

local trim_time = now - limit_interval
redis.call('ZREMRANGEBYSCORE', key, "-inf", trim_time)

local limited = 1
local count = redis.call('ZCARD', key)
if count < limit_events then
    redis.call('ZADD', key, now, now)
    redis.call('PEXPIRE', key, key_ttl)

    limited = 0
end

local min = tonumber(redis.call("ZRANGE", key, -limit_events, -limit_events)[1])
if not min then
    return { limited, limit_events - count - 1, 0 }
end
return { limited, 0, min - trim_time }
