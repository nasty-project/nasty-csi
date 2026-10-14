package retry

import (
	"errors"
	"fmt"
	"testing"

	nastyapi "github.com/nasty-project/nasty-go"
)

func TestUncertainMutationIsNeverReplayed(t *testing.T) {
	for _, config := range []Config{DefaultConfig(), DeletionConfig("delete"), {MaxAttempts: 3, RetryableFunc: func(error) bool { return true }}} {
		calls := 0
		cause := fmt.Errorf("%w: connection reset", nastyapi.ErrUncertainOutcome)
		err := WithRetryNoResult(t.Context(), config, func() error { calls++; return cause })
		if calls != 1 || !errors.Is(err, nastyapi.ErrUncertainOutcome) {
			t.Fatalf("uncertain mutation calls=%d, err=%v", calls, err)
		}
	}
}
