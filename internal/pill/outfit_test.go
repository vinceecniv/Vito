package pill

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// outfit.json is made from VM_ACC in the page (scripts/pill-outfit.mjs): the
// same pieces, and every one of them draws something.
func TestOutfitInStepWithPage(t *testing.T) {
	html, err := os.ReadFile("../../web/index.html")
	if err != nil {
		t.Skip(err)
	}
	s := string(html)
	at := strings.Index(s, "const VM_ACC = {")
	end := strings.Index(s[at:], "\n};")
	var page []string
	for _, m := range regexp.MustCompile(`(?m)^  (\w+): \{ name:`).FindAllStringSubmatch(s[at:at+end], -1) {
		page = append(page, m[1])
	}
	var ours []string
	for k, p := range outfitDefs() {
		ours = append(ours, k)
		if len(parsePiece(p.SVG, identity)) == 0 {
			t.Errorf("%s draws nothing", k)
		}
	}
	slices.Sort(page)
	slices.Sort(ours)
	if !slices.Equal(page, ours) {
		t.Errorf("outfit.json has %v, the page %v: run node scripts/pill-outfit.mjs", ours, page)
	}
}
