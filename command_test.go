package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestCommandTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, e := command(ctx, "", "sh", "-c", "sleep 10")
	if e == nil || !strings.Contains(e.Error(), "deadline exceeded") {
		t.Fatal(e)
	}
	if time.Since(start) > time.Second {
		t.Fatal("timeout left subprocess pipes open")
	}
}
