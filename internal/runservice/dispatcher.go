package runservice

import (
	"context"
	"fmt"
	"log"
	"runtime/debug"
)

type RunProcessor func(ctx context.Context, runID string) error

type InProcessDispatcher struct {
	ctx       context.Context
	queue     chan string
	processor RunProcessor
}

func NewInProcessDispatcher(ctx context.Context, processor RunProcessor) *InProcessDispatcher {
	dispatcher := &InProcessDispatcher{
		ctx:       ctx,
		queue:     make(chan string, 128),
		processor: processor,
	}

	go dispatcher.loop()
	return dispatcher
}

func (d *InProcessDispatcher) Enqueue(_ context.Context, runID string) error {
	select {
	case d.queue <- runID:
		return nil
	case <-d.ctx.Done():
		return d.ctx.Err()
	}
}

func (d *InProcessDispatcher) loop() {
	for {
		select {
		case <-d.ctx.Done():
			return
		case runID := <-d.queue:
			if err := d.processRun(runID); err != nil {
				log.Printf("asset-discovery run %s failed: %v", runID, err)
			}
		}
	}
}

func (d *InProcessDispatcher) processRun(runID string) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("panic while processing run %s: %v", runID, recovered)
			log.Printf("asset-discovery run %s panicked: %v\n%s", runID, recovered, debug.Stack())
		}
	}()

	return d.processor(d.ctx, runID)
}
