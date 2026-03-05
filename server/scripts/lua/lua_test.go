package lua

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func setupMiniredis(t *testing.T) (*miniredis.Miniredis, *redis.Client) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	return mr, rdb
}

func loadScript(t *testing.T) *redis.Script {
	t.Helper()
	luaBytes, err := os.ReadFile("check_and_set_seq.lua")
	if err != nil {
		t.Fatalf("failed to read lua script: %v", err)
	}
	return redis.NewScript(string(luaBytes))
}

func TestCheckAndSetSeq_OK(t *testing.T) {
	_, rdb := setupMiniredis(t)
	defer rdb.Close()
	script := loadScript(t)
	ctx := context.Background()

	planID := "test-plan"
	keys := []string{"seq:" + planID, "aborted:" + planID}

	// First event (seq 1) -> OK
	res, err := script.Run(ctx, rdb, keys, 1).Text()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res != "OK" {
		t.Errorf("expected OK, got %v", res)
	}

	// Second event (seq 2) -> OK
	res, err = script.Run(ctx, rdb, keys, 2).Text()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res != "OK" {
		t.Errorf("expected OK, got %v", res)
	}

	// Verify Redis state
	val, _ := rdb.Get(ctx, "seq:"+planID).Int()
	if val != 2 {
		t.Errorf("expected seq counter to be 2, got %d", val)
	}
}

func TestCheckAndSetSeq_Duplicate(t *testing.T) {
	_, rdb := setupMiniredis(t)
	defer rdb.Close()
	script := loadScript(t)
	ctx := context.Background()

	planID := "test-dup"
	keys := []string{"seq:" + planID, "aborted:" + planID}

	// Process seq 1
	script.Run(ctx, rdb, keys, 1)

	// Duplicate seq 1
	res, err := script.Run(ctx, rdb, keys, 1).Text()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res != "DUPLICATE" {
		t.Errorf("expected DUPLICATE, got %v", res)
	}
}

func TestCheckAndSetSeq_OutOfOrder(t *testing.T) {
	_, rdb := setupMiniredis(t)
	defer rdb.Close()
	script := loadScript(t)
	ctx := context.Background()

	planID := "test-ooo"
	keys := []string{"seq:" + planID, "aborted:" + planID}

	// Process seq 1
	script.Run(ctx, rdb, keys, 1)

	// Skip to seq 5 (gap)
	res, err := script.Run(ctx, rdb, keys, 5).Text()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res != "OUT_OF_ORDER" {
		t.Errorf("expected OUT_OF_ORDER, got %v", res)
	}

	// Seq counter should still be 1
	val, _ := rdb.Get(ctx, "seq:"+planID).Int()
	if val != 1 {
		t.Errorf("expected seq counter to remain 1, got %d", val)
	}
}

func TestCheckAndSetSeq_Aborted(t *testing.T) {
	mr, rdb := setupMiniredis(t)
	defer rdb.Close()
	script := loadScript(t)
	ctx := context.Background()

	planID := "test-abort"
	keys := []string{"seq:" + planID, "aborted:" + planID}

	// Process seq 1 first
	script.Run(ctx, rdb, keys, 1)

	// Set abort flag
	mr.Set("aborted:"+planID, "1")
	mr.SetTTL("aborted:"+planID, 10*time.Second)

	// Any seq after abort -> ABORTED
	res, err := script.Run(ctx, rdb, keys, 2).Text()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res != "ABORTED" {
		t.Errorf("expected ABORTED, got %v", res)
	}
}

func TestCheckAndSetSeq_FullSequence(t *testing.T) {
	_, rdb := setupMiniredis(t)
	defer rdb.Close()
	script := loadScript(t)
	ctx := context.Background()

	planID := "test-full"
	keys := []string{"seq:" + planID, "aborted:" + planID}

	// Process 5 events in order
	expected := []string{"OK", "OK", "OK", "OK", "OK"}
	for i := 1; i <= 5; i++ {
		res, err := script.Run(ctx, rdb, keys, i).Text()
		if err != nil {
			t.Fatalf("seq %d: unexpected error: %v", i, err)
		}
		if res != expected[i-1] {
			t.Errorf("seq %d: expected %s, got %s", i, expected[i-1], res)
		}
	}

	// Verify final state
	val, _ := rdb.Get(ctx, "seq:"+planID).Int()
	if val != 5 {
		t.Errorf("expected final seq counter to be 5, got %d", val)
	}
}
