package pglogwatch

import (
	"context"
	"io"
)

// open is implemented in T098.
func (fs *FileSet) open(ctx context.Context) (io.ReadCloser, error) {
	_ = ctx
	return nil, ErrNotSeekable
}
