package cleanup

import (
	"strings"
	"testing"
	"time"
)

func TestTimeoutGrowsWithLength(t *testing.T) {
	words := func(n int) string { return strings.TrimSpace(strings.Repeat("woord ", n)) }
	for _, c := range []struct {
		ms, words int
		want      time.Duration
	}{
		{5000, 40, 5 * time.Second},
		{5000, 100, 5 * time.Second},
		{5000, 330, 5*time.Second + 230*20*time.Millisecond},
		{0, 10, 5 * time.Second},
		{5000, 10000, 60 * time.Second},
	} {
		if got := Timeout(c.ms, words(c.words)); got != c.want {
			t.Errorf("Timeout(%d, %d words) = %v, want %v", c.ms, c.words, got, c.want)
		}
	}
}
