//go:build e2e
// +build e2e

package framework

// FindCondition returns the first condition matching ctype, or nil if
// none is present.
func FindCondition(conds []HarnessRunCondition, ctype string) *HarnessRunCondition {
	for i := range conds {
		if conds[i].Type == ctype {
			return &conds[i]
		}
	}
	return nil
}
