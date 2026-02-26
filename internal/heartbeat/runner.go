package heartbeat

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync/atomic"
	"time"

	"github.com/robfig/cron/v3"
)

type Runner struct {
	cron     *cron.Cron
	running  atomic.Bool
	handler  func(context.Context) error
	timezone string
}

func NewRunner(spec string, timezone string, handler func(context.Context) error) (*Runner, error) {
	if strings.TrimSpace(spec) == "" {
		return nil, fmt.Errorf("heartbeat cron spec is required")
	}
	if strings.TrimSpace(timezone) == "" {
		return nil, fmt.Errorf("heartbeat timezone is required")
	}
	if handler == nil {
		return nil, fmt.Errorf("heartbeat handler is required")
	}

	loc, err := time.LoadLocation(timezone)
	if err != nil {
		return nil, fmt.Errorf("load heartbeat timezone: %w", err)
	}

	scheduler := cron.New(cron.WithLocation(loc), cron.WithSeconds())
	r := &Runner{
		cron:     scheduler,
		handler:  handler,
		timezone: timezone,
	}
	if _, err := scheduler.AddFunc(spec, r.execute); err != nil {
		return nil, fmt.Errorf("register heartbeat cron: %w", err)
	}
	return r, nil
}

func (r *Runner) Start(ctx context.Context) {
	r.cron.Start()
	go func() {
		<-ctx.Done()
		stopCtx := r.cron.Stop()
		<-stopCtx.Done()
	}()
}

func (r *Runner) execute() {
	if !r.running.CompareAndSwap(false, true) {
		log.Printf("heartbeat skipped: reason=already_running timezone=%s", r.timezone)
		return
	}
	defer r.running.Store(false)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	if err := r.handler(ctx); err != nil {
		log.Printf("heartbeat failed: timezone=%s err=%v", r.timezone, err)
	}
}
