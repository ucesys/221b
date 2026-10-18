package auth

// Current is the session as it stands now, for a response that stays open
// such as an event stream: ok is false once the session has ended (signed
// out, reset, disabled, deleted or expired) or its person may no longer use
// the console (RB-05, RB-23, RB-46). Looking does not count as activity.
func (s *Service) Current(sess Session) (Session, bool) {
	if s == nil || s.cfg.Mode == ModeNone || sess.ID == "" {
		return sess, true
	}
	cur, ok := s.sessions.peek(sess.ID)
	if !ok {
		return Session{}, false
	}
	return s.live(cur)
}

// peek is Get without counting as activity.
func (s *Sessions) peek(id string) (Session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.byID[id]
	if !ok {
		return Session{}, false
	}
	now := s.now()
	if now.Sub(sess.LastSeen) > s.idle || now.After(sess.Expires) {
		return Session{}, false
	}
	return *sess, true
}
