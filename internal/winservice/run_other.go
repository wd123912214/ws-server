//go:build !windows

package winservice

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

func Run(_ string, run func(context.Context) error) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return run(ctx)
}
