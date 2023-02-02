package demo_sequential_tests

import (
	"fmt"
	"math/rand"
	"sync/atomic"
	"testing"
	"time"
)

func Test(t *testing.T) {
	for _, name := range []string{"sequential", "parallel"} {
		start := time.Now()
		t.Run(name, func(t *testing.T) {
			var running int64
			for i := 0; i < 4; i++ {
				t.Run(fmt.Sprintf("%d", i), func(t *testing.T) {
					if name == "parallel" {
						t.Parallel()
					}
					atomic.AddInt64(&running, 1)
					t.Logf("%d tests are run concurrently", running)
					time.Sleep(time.Duration(rand.Intn(200)) * time.Millisecond)
					atomic.AddInt64(&running, -1)
				})
			}
		})
		t.Logf("%s test took %s", name, time.Since(start))
	}
}
