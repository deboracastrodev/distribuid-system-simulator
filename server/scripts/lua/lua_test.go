package lua

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// This test requires a running Redis instance or a mock.
// Since we don't have miniredis in go.mod, this is a placeholder
// for how the test should look. In a real environment, we'd use miniredis.
func TestCheckAndSetSeq(t *testing.T) {
	// Skip if REDIS_ADDR is not set
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		t.Skip("Skipping Lua test: REDIS_ADDR not set")
	}

	ctx := context.Background()
	rdb := redis.NewClient(&redis.Options{Addr: addr})
	defer rdb.Close()

	scriptPath := "check_and_set_seq.lua"
	luaBytes, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("failed to read lua script: %v", err)
	}
	script := redis.NewScript(string(luaBytes))

	planID := "test-plan"
	keys := []string{"seq:" + planID, "aborted:" + planID}

	// Case 1: OK (1st event)
	res, err := script.Run(ctx, rdb, keys, 1).Text()
	if err != nil || res != "OK" {
		t.Errorf("expected OK, got %v (err: %v)", res, err)
	}

	// Case 2: OK (2nd event)
	res, err = script.Run(ctx, rdb, keys, 2).Text()
	if err != nil || res != "OK" {
		t.Errorf("expected OK, got %v (err: %v)", res, err)
	}

	// Case 3: DUPLICATE
	res, err = script.Run(ctx, rdb, keys, 2).Text()
	if err != nil || res != "DUPLICATE" {
		t.Errorf("expected DUPLICATE, got %v", res)
	}

	// Case 4: OUT_OF_ORDER
	res, err = script.Run(ctx, rdb, keys, 5).Text()
	if err != nil || res != "OUT_OF_ORDER" {
		t.Errorf("expected OUT_OF_ORDER, got %v", res)
	}

	// Case 5: ABORTED
	rdb.Set(ctx, "aborted:"+planID, "1", 10*time.Second)
	res, err = script.Run(ctx, rdb, keys, 3).Text()
	if err != nil || res != "ABORTED" {
		t.Errorf("expected ABORTED, got %v", res)
	}

	// Cleanup
	rdb.Del(ctx, keys...)
}
