package torrstor

import (
	"testing"
	"time"
)

// MaxReaderIdleSec is the signal the pause preload keys off, so its edge cases
// decide whether a paused viewer gets buffer or does not. Every case below is a
// property that, if wrong, either starves a paused viewer or inflates the
// window during ordinary playback.
func TestMaxReaderIdleSec(t *testing.T) {
	if got := (*Cache)(nil).MaxReaderIdleSec(); got != 0 {
		t.Errorf("nil cache: got %d, want 0", got)
	}

	cases := []struct {
		name  string
		setup func(*Cache)
		want  int64
	}{
		{
			// No readers at all: nothing is watching, nothing is idle. This is
			// the number the update loop reads on an idle server, and a
			// non-zero value here would inflate the window with no viewer
			// present.
			name:  "no readers reports zero",
			setup: func(c *Cache) {},
			want:  0,
		},
		{
			name: "a reader that just read reports zero",
			setup: func(c *Cache) {
				c.readers = map[*Reader]struct{}{
					{isUse: true, lastAccess: time.Now().Unix()}: {},
				}
			},
			want: 0,
		},
		{
			// The heart of it: one silent reader is what "paused" looks like
			// from inside the server.
			name: "one idle reader reports its silence",
			setup: func(c *Cache) {
				c.readers = map[*Reader]struct{}{
					{isUse: true, lastAccess: time.Now().Unix() - 30}: {},
				}
			},
			want: 30,
		},
		{
			// It must be the WORST reader, not the average or the last one:
			// one viewer paused while another keeps watching is the common case
			// on a shared server, and averaging would hide the pause.
			name: "reports the most idle reader, not the average",
			setup: func(c *Cache) {
				now := time.Now().Unix()
				c.readers = map[*Reader]struct{}{
					{isUse: true, lastAccess: now}:      {},
					{isUse: true, lastAccess: now - 90}: {},
					{isUse: true, lastAccess: now - 5}:  {},
				}
			},
			want: 90,
		},
		{
			// A parked reader is not a waiting viewer — checkReader parks it
			// after a silence when a cache has more than one reader. Counting it
			// would keep boosting the window of a viewer nobody is watching.
			name: "parked readers are ignored",
			setup: func(c *Cache) {
				c.readers = map[*Reader]struct{}{
					{isUse: true, lastAccess: time.Now().Unix()}:        {},
					{isUse: false, lastAccess: time.Now().Unix() - 600}: {},
				}
			},
			want: 0,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cache := &Cache{readers: map[*Reader]struct{}{}}
			c.setup(cache)
			if got := cache.MaxReaderIdleSec(); got != c.want {
				t.Errorf("got %d, want %d", got, c.want)
			}
		})
	}
}
