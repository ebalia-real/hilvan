package core

import (
	"context"
	"errors"
)

var errSSSUnsupported = errors.New("sliding sync no soportado")

type slidingSync struct{}

func (a *Account) wantSlidingSync(ctx context.Context) bool { return false }

func (a *Account) runSlidingSync(ctx context.Context) error { return errSSSUnsupported }
