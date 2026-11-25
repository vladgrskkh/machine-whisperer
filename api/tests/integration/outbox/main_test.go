//go:build integration

package outbox_test

import (
	"os"
	"testing"

	"github.com/vladgrskkh/machine-whisperer/api/internal/testutil"
)

func TestMain(m *testing.M) {
	os.Exit(testutil.RunWithPostgres(m))
}
