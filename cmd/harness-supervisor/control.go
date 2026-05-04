package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
)

// ctlMessage is the wire shape of one newline-delimited JSON ctl frame.
type ctlMessage struct {
	Action string `json:"action"`
	Reason string `json:"reason,omitempty"`
	Seq    int32  `json:"seq,omitempty"`
}

// readCtl decodes ctl frames from c and emits them on out until c is
// closed or ctx is canceled. Returns nil on graceful EOF, the underlying
// error otherwise.
func readCtl(ctx context.Context, c net.Conn, out chan<- ctlMessage) error {
	defer close(out)
	dec := json.NewDecoder(bufio.NewReader(c))
	for {
		var msg ctlMessage
		if err := dec.Decode(&msg); err != nil {
			if errors.Is(err, io.EOF) || ctx.Err() != nil {
				return nil
			}
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case out <- msg:
		}
	}
}
