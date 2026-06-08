package zlibMath

import (
	"time"
)

func InitDefaultMin[T time.Duration | uint64](ptr *T, def T, min T) {
	if *ptr == 0 {
		*ptr = def
	}
	if *ptr < min {
		*ptr = min
	}
}

