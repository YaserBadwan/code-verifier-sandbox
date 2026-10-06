package sandbox

import (
	"bytes"
	"errors"
)

var errOutputLimit = errors.New("output limit exceeded")

// cappedBuffer keeps at most limit bytes and fails the write that goes over,
// which stops stdcopy so a program printing forever can't eat our memory.
type cappedBuffer struct {
	buf      bytes.Buffer
	limit    int64
	exceeded bool
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	room := c.limit - int64(c.buf.Len())
	if int64(len(p)) > room {
		if room > 0 {
			c.buf.Write(p[:room])
		}
		c.exceeded = true
		return int(max(room, 0)), errOutputLimit
	}
	return c.buf.Write(p)
}

func (c *cappedBuffer) Bytes() []byte {
	return c.buf.Bytes()
}
