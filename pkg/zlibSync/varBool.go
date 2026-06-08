package zlibSync

import (
	"sync"
)

type Bool struct {
	locker sync.Mutex
	v      bool
}

func (s *Bool) Get() bool {
	s.locker.Lock()
	out := s.v
	s.locker.Unlock()
	return out
}

func (s *Bool) Set(v bool) {
	s.locker.Lock()
	s.v = v
	s.locker.Unlock()
}

func (s *Bool) SetTrue() {
	s.Set(true)
}

func (s *Bool) SetFalse() {
	s.Set(false)
}

func (s *Bool) SetAndReturnOld(v bool) bool {
	s.locker.Lock()
	old := s.v
	s.v = v
	s.locker.Unlock()
	return old
}

