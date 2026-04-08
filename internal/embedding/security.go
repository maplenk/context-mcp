package embedding

import (
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
)

func discardAndClose(label string, resp *http.Response) error {
	if resp == nil || resp.Body == nil {
		return nil
	}
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		_ = resp.Body.Close()
		return fmt.Errorf("draining %s response body: %w", label, err)
	}
	if err := resp.Body.Close(); err != nil {
		return fmt.Errorf("closing %s response body: %w", label, err)
	}
	return nil
}

func readLocalModelFile(path string) ([]byte, error) {
	// #nosec G304 -- path comes from explicit local model/tokenizer configuration.
	return os.ReadFile(path)
}

func clampUint32Len(n int) uint32 {
	if n <= 0 {
		return 0
	}
	if uint64(n) > math.MaxUint32 {
		return math.MaxUint32
	}
	// #nosec G115 -- n is range-checked and clamped before conversion.
	return uint32(n)
}

func clampInt32(v int) int32 {
	if v < math.MinInt32 {
		return math.MinInt32
	}
	if v > math.MaxInt32 {
		return math.MaxInt32
	}
	// #nosec G115 -- v is range-checked and clamped before conversion.
	return int32(v)
}
