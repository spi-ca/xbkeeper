package packaging

import (
	"fmt"
	"os"
	"testing"
)

// Packaging tests inspect the checkout or extracted source root, not installed
// system files. Keep relative fixture paths identical in both environments.
func TestMain(m *testing.M) {
	if err := os.Chdir("../.."); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}
