package ui

// reloadCoalescer serializes storage-watcher reloads. The watcher can report
// several writes while one registry snapshot is still hydrating; those writes
// only need one latest-state follow-up load.
//
// It is owned by the Bubble Tea event-loop goroutine, so it deliberately has
// no mutex or atomics. Commands do the I/O, but request/complete are called by
// message handling on the event loop.
type reloadCoalescer struct {
	inFlight bool
	pending  bool
}

// request starts a reload when none is running. A request during a reload is
// remembered and returns false so the caller can avoid launching duplicate I/O.
func (c *reloadCoalescer) request() bool {
	if c.inFlight {
		c.pending = true
		return false
	}
	c.inFlight = true
	return true
}

// complete ends the current reload and reports whether exactly one follow-up
// should be started for requests coalesced while it was running.
func (c *reloadCoalescer) complete() bool {
	if c.pending {
		c.pending = false
		return true
	}
	c.inFlight = false
	return false
}
