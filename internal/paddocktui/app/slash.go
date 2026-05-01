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

package app

import "strings"

// SlashCmd is a recognised slash command. SlashNone means the input
// was an ordinary prompt; SlashUnknown means the input was a `:`-
// prefixed token we don't recognise.
type SlashCmd int

const (
	SlashNone SlashCmd = iota
	SlashCancel
	SlashQueue
	SlashEdit
	SlashStatus
	SlashTemplate
	SlashInteractive
	SlashHelp
	SlashUnknown
)

// ParseSlash classifies an input line. Returns (cmd, arg, isSlash).
// When isSlash is false, the input is a regular prompt and the
// caller should treat arg as the prompt body.
func ParseSlash(input string) (SlashCmd, string, bool) {
	in := strings.TrimSpace(input)
	if !strings.HasPrefix(in, ":") || len(in) <= 1 {
		return SlashNone, input, false
	}
	rest := strings.TrimSpace(in[1:])
	parts := strings.SplitN(rest, " ", 2)
	head := parts[0]
	arg := ""
	if len(parts) == 2 {
		arg = strings.TrimSpace(parts[1])
	}
	switch head {
	case "cancel":
		return SlashCancel, "", true
	case "queue":
		return SlashQueue, "", true
	case "edit":
		return SlashEdit, "", true
	case "status":
		return SlashStatus, "", true
	case "template":
		return SlashTemplate, arg, true
	case "interactive":
		return SlashInteractive, "", true
	case "help":
		return SlashHelp, "", true
	default:
		return SlashUnknown, head, true
	}
}
