-- advance_seq.lua
-- Moves a plan's cached sequence counter forward, never backward. The counter
-- mirrors what Postgres already committed, so the compare-and-set has to be
-- atomic: a late or concurrent writer must not roll it back.
--
-- KEYS[1] = seq:<plan_id>
-- ARGV[1] = seq_id committed in Postgres
-- ARGV[2] = TTL in seconds
--
-- Returns the counter value after the call.

local current = tonumber(redis.call("GET", KEYS[1]) or "0") or 0
local incoming = tonumber(ARGV[1])

if incoming > current then
    redis.call("SET", KEYS[1], incoming, "EX", tonumber(ARGV[2]))
    return incoming
end
return current
