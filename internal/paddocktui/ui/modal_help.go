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

package ui

import "strings"

func HelpModalView() string {
	body := strings.Join([]string{
		"paddock-tui keybindings",
		"",
		"sidebar:",
		"  ↑↓ / jk    move selection",
		"  Enter      focus session",
		"  n          new session",
		"  e          end session",
		"  /          filter",
		"  q          quit",
		"",
		"prompt:",
		"  Enter      submit prompt (queues if a run is in flight)",
		"  Ctrl-J     newline (multi-line prompt)",
		"  Ctrl-E     open $EDITOR",
		"  Ctrl-X     cancel in-flight run",
		"  Ctrl-R     toggle raw events",
		"",
		"slash commands (in prompt):",
		"  :cancel     cancel in-flight run",
		"  :queue      show queued prompts",
		"  :template T set last-template",
		"  :status     compact session summary",
		"  :interactive (reserved — not yet implemented)",
		"  :help       this screen",
		"",
		"Esc / ?: close this help",
	}, "\n")
	return StyleModalFrame.Render(body)
}
