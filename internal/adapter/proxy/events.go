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
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"

	paddockv1alpha1 "github.com/tjorri/paddock/api/v1alpha1"
)

// runDataReader reads the data UDS line-by-line, broadcasts each
// line to subscribers (for /stream WS clients) and translates each
// line via cfg.Converter, appending results to cfg.EventsPath.
//
// Returns when the data UDS read returns an error (typically EOF
// after the supervisor closes the connection).
func runDataReader(r io.Reader, fan *fanout, eventsPath string, conv func(string) ([]paddockv1alpha1.PaddockEvent, error)) error {
	var (
		out *os.File
		enc *json.Encoder
	)
	if eventsPath != "" && conv != nil {
		if err := os.MkdirAll(filepath.Dir(eventsPath), 0o755); err != nil {
			return fmt.Errorf("mkdir events: %w", err)
		}
		f, err := os.OpenFile(eventsPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return fmt.Errorf("open events: %w", err)
		}
		out = f
		enc = json.NewEncoder(out)
		defer func() { _ = out.Close() }()
	}

	br := bufio.NewReader(r)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			fan.broadcast(line)
			if enc != nil {
				events, cerr := conv(string(line))
				if cerr != nil {
					log.Printf("convert line: %v", cerr)
				}
				for _, ev := range events {
					if werr := enc.Encode(ev); werr != nil {
						log.Printf("write event: %v", werr)
						break
					}
				}
				if len(events) > 0 {
					_ = out.Sync()
				}
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("read data UDS: %w", err)
		}
	}
}
