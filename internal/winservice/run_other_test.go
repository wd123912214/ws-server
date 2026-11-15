//go:build !windows

package winservice

import (
	"context"
	"testing"
)

func TestRunInvokesConsoleCallback(t *testing.T) {
	called := false
	if err := Run("ignored", func(context.Context) error {
		called = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("callback was not invoked")
	}
}
