-- check_and_set_seq.lua
-- Atomic check-and-set for event sequence validation.
--
-- KEYS[1] = seq:<plan_id>       (last processed seq_id)
-- KEYS[2] = aborted:<plan_id>   (abort flag)
-- ARGV[1] = seq_id to validate
--
-- Returns:
--   "OK"           - seq_id accepted, counter updated
--   "DUPLICATE"    - seq_id already processed
--   "OUT_OF_ORDER" - seq_id is ahead (gap detected)
--   "ABORTED"      - plan was aborted

-- Check if plan is aborted
if redis.call("EXISTS", KEYS[2]) == 1 then
    return "ABORTED"
end

local current = tonumber(redis.call("GET", KEYS[1]) or "0")
if current == nil then
    current = 0
end

local incoming = tonumber(ARGV[1])

if incoming == current + 1 then
    redis.call("SET", KEYS[1], incoming)
    return "OK"
elseif incoming <= current then
    return "DUPLICATE"
else
    return "OUT_OF_ORDER"
end
