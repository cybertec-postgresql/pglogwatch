package pglogwatch

import (
	"context"
	"io"
)

// ParallelScan is implemented in T106/T107.
func ParallelScan(ctx context.Context, srcs []io.ReaderAt, cfg Config, workers int,
	fn func(worker int, r *Record) error) error {
	_, _, _, _, _ = ctx, srcs, cfg, workers, fn
	return nil
}
