package sherlock

// MaxBreakpoints is the most cache_control markers the Messages API takes
// in one request, the tools', the system blocks' and the messages'
// together; a fifth is refused.
const MaxBreakpoints = 4

// cacheable says whether a block may carry cache_control: a thinking
// block may not, nor an empty text block.
func cacheable(b Block) bool {
	switch b.Type {
	case "text":
		return b.Text != ""
	case "tool_use", "tool_result", "image", "document":
		return true
	}
	return false
}

// withHistoryCache marks the conversation's history for the prompt cache
// (the measurement of November 23, 2026, F5; AI-50), with the breakpoints
// the tools and the system blocks leave free: first the newest message,
// so that each round and the next turn read everything before it from
// the cache; then the last message before this turn's question
// (turnStart, the question's index; -1 for none), which a conversation
// rebuilt from what it keeps (CF-47) still starts with at its next turn.
// An effort message (empty) carries none; neither does a thinking block.
// The request's messages are copied where marked, never changed in place.
func withHistoryCache(req Request, turnStart int) Request {
	free := MaxBreakpoints
	for _, t := range req.Tools {
		if t.CacheControl != nil {
			free--
		}
	}
	for _, b := range req.System {
		if b.CacheControl != nil {
			free--
		}
	}
	for _, m := range req.Messages {
		for _, b := range m.Content {
			if b.CacheControl != nil {
				free--
			}
		}
	}
	if free <= 0 || len(req.Messages) == 0 {
		return req
	}
	msgs := append([]Message(nil), req.Messages...)
	mark := func(at int) bool {
		for ; at >= 0; at-- {
			if len(msgs[at].Content) > 0 {
				break
			}
		}
		if at < 0 {
			return false
		}
		content := msgs[at].Content
		for i := len(content) - 1; i >= 0; i-- {
			if content[i].CacheControl != nil {
				return false
			}
			if cacheable(content[i]) {
				c := append([]Block(nil), content...)
				c[i].CacheControl = &CacheControl{Type: "ephemeral"}
				msgs[at].Content = c
				return true
			}
		}
		return false
	}
	if mark(len(msgs) - 1) {
		free--
	}
	if free > 0 && turnStart > 0 && turnStart < len(msgs) {
		mark(turnStart - 1)
	}
	req.Messages = msgs
	return req
}
