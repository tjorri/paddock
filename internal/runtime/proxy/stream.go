/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package proxy

import (
	"context"
	"net/http"

	"github.com/coder/websocket"
)

const streamSubprotocol = "paddock.stream.v1"

// streamHandler returns the /stream WebSocket handler that bridges
// the client WS to the data UDS bidirectionally:
//   - inbound (client -> server) frames write to the data UDS directly
//   - outbound (server -> client) lines come from a fanout subscription
//     fed by runDataReader, which is the single owner of data-UDS reads.
func (s *Server) streamHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			Subprotocols: []string{streamSubprotocol},
		})
		if err != nil {
			return
		}
		defer func() { _ = c.CloseNow() }()

		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()

		ch := s.fanout.subscribe()
		defer s.fanout.unsubscribe(ch)

		// fanout -> client WS
		go func() {
			defer cancel()
			for {
				select {
				case <-ctx.Done():
					return
				case line, ok := <-ch:
					if !ok {
						return
					}
					if werr := c.Write(ctx, websocket.MessageText, line); werr != nil {
						return
					}
				}
			}
		}()

		// client WS -> data UDS
		for {
			_, msg, err := c.Read(ctx)
			if err != nil {
				return
			}
			s.dataWriteMu.Lock()
			_, werr := s.dataConn.Write(msg)
			s.dataWriteMu.Unlock()
			if werr != nil {
				return
			}
		}
	})
}
