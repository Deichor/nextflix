package cache

import (
	"context"
	"testing"
)

func TestNewKeepsClientWhenRedisIsDown(t *testing.T) {
	c, err := New("redis://127.0.0.1:1/0")
	if err != nil || c == nil || c.Redis() == nil {
		t.Fatalf("New: %v %v", c, err)
	}
	defer c.Close()
	if err := c.Ping(context.Background()); err == nil {
		t.Fatal("ping to a closed port succeeded")
	}
	// Misses instead of panics while redis is down
	var v int
	if ok, _ := c.GetJSON(context.Background(), "k", &v); ok {
		t.Fatal("hit without redis")
	}
}

func TestNewDisabledAndBadURL(t *testing.T) {
	if c, err := New(""); c != nil || err != nil {
		t.Fatalf("empty url: %v %v", c, err)
	}
	if _, err := New("not a url"); err == nil {
		t.Fatal("bad url accepted")
	}
	var c *Cache
	if c.Ping(context.Background()) == nil {
		t.Fatal("nil cache pinged fine")
	}
}
