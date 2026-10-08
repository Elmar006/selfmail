package application

import (
	"bytes"
	"context"
	"fmt"
)

type boundedBuffer struct {
	buffer bytes.Buffer
	limit  int
	ctx    context.Context
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if b.ctx != nil && b.ctx.Err() != nil {
		return 0, b.ctx.Err()
	}
	if b.buffer.Len()+len(p) > b.limit {
		return 0, fmt.Errorf("rendered template exceeds %d bytes", b.limit)
	}
	return b.buffer.Write(p)
}
func (b *boundedBuffer) String() string { return b.buffer.String() }
