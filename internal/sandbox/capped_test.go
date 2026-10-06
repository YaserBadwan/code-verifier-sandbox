package sandbox

import (
	"errors"
	"testing"
)

func TestCappedBuffer(t *testing.T) {
	c := &cappedBuffer{limit: 5}
	if n, err := c.Write([]byte("abc")); n != 3 || err != nil {
		t.Fatalf("first write got %d %v", n, err)
	}
	n, err := c.Write([]byte("defg"))
	if !errors.Is(err, errOutputLimit) || n != 2 {
		t.Fatalf("overflowing write got %d %v", n, err)
	}
	if string(c.Bytes()) != "abcde" || !c.exceeded {
		t.Fatalf("expected the first 5 bytes kept, got %q exceeded=%v", c.Bytes(), c.exceeded)
	}
	if n, err := c.Write([]byte("x")); n != 0 || !errors.Is(err, errOutputLimit) {
		t.Fatalf("write after full got %d %v", n, err)
	}
}

func TestCappedBufferExactFit(t *testing.T) {
	c := &cappedBuffer{limit: 3}
	if _, err := c.Write([]byte("abc")); err != nil {
		t.Fatalf("writing exactly the limit should be fine, got %v", err)
	}
	if c.exceeded {
		t.Fatal("should not be marked exceeded")
	}
}
