package apps

import "time"

// SetRenderTimeout changes how long a render may take, until the returned
// function puts it back.
func SetRenderTimeout(d time.Duration) func() {
	was := renderTimeout
	renderTimeout = d
	return func() { renderTimeout = was }
}

// RenderHere renders in the test's own process.
var RenderHere = renderHere
