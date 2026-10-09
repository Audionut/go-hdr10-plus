package extract

import (
	"errors"
	"strings"
	"testing"
)

func TestRetainedStorageFailureDiagnostics(t *testing.T) {
	for _, scope := range []string{"operation", "parent", "shared"} {
		t.Run(scope, func(t *testing.T) {
			budgetMax := int64(4096)
			if scope == "shared" {
				budgetMax = 1024
			}
			budget, _ := NewMemoryBudget(budgetMax)
			a := &accounting{max: 4096, shared: budget}
			if scope == "operation" {
				a.max = 1024
			} else if scope == "parent" {
				a.parent = &accounting{max: 1024}
			}
			defer a.release()
			if _, err := a.reserve(640); err != nil {
				t.Fatal(err)
			}
			_, err := a.reserve(256)
			if !errors.Is(err, ErrResourceLimit) {
				t.Fatal(err)
			}
			for _, value := range []string{scope + " retained storage", "request=", "used=768", "limit=1024"} {
				if !strings.Contains(err.Error(), value) {
					t.Fatalf("missing quota diagnosis %q: %v", value, err)
				}
			}
			used, peak := budget.Usage()
			if used != 768 || peak != 768 || a.used != 768 {
				t.Fatalf("rejected reservation changed accounting: used=%d peak=%d local=%d", used, peak, a.used)
			}
		})
	}
}
