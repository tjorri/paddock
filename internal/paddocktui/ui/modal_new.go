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

import (
	"fmt"
	"strings"

	"github.com/tjorri/paddock/internal/paddocktui/app"
)

// NewSessionModalView renders the new-session modal. It takes the
// whole Model (rather than just the modal state) so it can show the
// target namespace at the top — sessions created here land in
// m.Namespace, so making that visible BEFORE submission avoids
// surprise admission rejections.
func NewSessionModalView(m app.Model) string {
	s := m.ModalNew
	if s == nil {
		return ""
	}
	field := func(label, value string, active bool) string {
		marker := "  "
		if active {
			marker = "▸ "
		}
		return fmt.Sprintf("%s%s: %s", marker, label, value)
	}
	tmpl := ""
	if len(s.TemplatePicks) > 0 {
		tmpl = s.TemplatePicks[s.TemplateIdx]
	}
	ns := m.Namespace
	if ns == "" {
		ns = "default"
	}
	body := strings.Join([]string{
		"Namespace: " + ns,
		"─────────────────────────────",
		field("name", s.NameInput, s.Field == 0),
		field("template", tmpl, s.Field == 1),
		field("storage", s.StorageInput, s.Field == 2),
		field("seed-repo", s.SeedRepoInput, s.Field == 3),
		"",
		"Tab/Shift-Tab: switch field · Enter on last field: submit · Esc: cancel",
	}, "\n")
	return StyleModalFrame.Render(body)
}
