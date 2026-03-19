package nodes

import (
	"fmt"
	"sync/atomic"
	"time"
)

var nodeIDCounter atomic.Uint64

func newNodeID(prefix string) string {
	return fmt.Sprintf("%s-%d-%d", prefix, time.Now().UnixNano(), nodeIDCounter.Add(1))
}
