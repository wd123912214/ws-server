//go:build windows

package winservice

import (
	"context"
	"errors"
	"os"
	"os/signal"

	"golang.org/x/sys/windows/svc"
)

func Run(name string, run func(context.Context) error) error {
	isService, err := svc.IsWindowsService()
	if err != nil {
		return err
	}
	if !isService {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		return run(ctx)
	}
	return svc.Run(name, &serviceHandler{run: run})
}

type serviceHandler struct {
	run func(context.Context) error
}

func (h *serviceHandler) Execute(_ []string, requests <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- h.run(ctx) }()
	status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for {
		select {
		case request := <-requests:
			switch request.Cmd {
			case svc.Interrogate:
				status <- request.CurrentStatus
			case svc.Stop, svc.Shutdown:
				status <- svc.Status{State: svc.StopPending}
				cancel()
				if err := <-errCh; err != nil && !errors.Is(err, context.Canceled) {
					return false, 1
				}
				return false, 0
			}
		case err := <-errCh:
			if err != nil && !errors.Is(err, context.Canceled) {
				return false, 1
			}
			return false, 0
		}
	}
}
