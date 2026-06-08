package zlibSync

import (
	"sync"
	"time"
)

type Time struct {
	locker sync.Mutex
	v      time.Time
}

func (s *Time) Get() time.Time {
	s.locker.Lock()
	out := s.v
	s.locker.Unlock()
	return out
}

func (s *Time) Set(v time.Time) {
	s.locker.Lock()
	s.v = v
	s.locker.Unlock()
}

func (s *Time) SetNow() {
	s.Set(time.Now())
}

