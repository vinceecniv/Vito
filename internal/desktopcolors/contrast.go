package desktopcolors

import "math"

// minContrast is WCAG AA for text: what a label on a primary button and a
// switch's knob on primary keep at least.
const minContrast = 4.5

// readable makes every side's on_primary legible on its primary: the shell's
// own when it keeps minContrast (DMS's dark text on a light primary), else
// white or a deep shade of primary, whichever stands out more. A side without
// a primary (Omarchy's other side gets one) is left alone.
func readable(p *Palette) *Palette {
	if p == nil {
		return nil
	}
	for _, side := range []map[string]string{p.Light, p.Dark} {
		pr := side["primary"]
		if !reHex.MatchString(pr) {
			continue
		}
		if on := side["on_primary"]; reHex.MatchString(on) && Contrast(on, pr) >= minContrast {
			continue
		}
		dark := mixHex(pr, "#000000", .78)
		if Contrast("#ffffff", pr) >= Contrast(dark, pr) {
			side["on_primary"] = "#ffffff"
		} else {
			side["on_primary"] = dark
		}
	}
	return p
}

// Contrast is WCAG's contrast ratio of two #rrggbb colours, 1 to 21.
func Contrast(a, b string) float64 {
	x, y := luminance(a), luminance(b)
	return (math.Max(x, y) + .05) / (math.Min(x, y) + .05)
}

func luminance(h string) float64 {
	c := parseHex(h)
	var l [3]float64
	for i, v := range c {
		f := float64(v) / 255
		if f <= .03928 {
			l[i] = f / 12.92
		} else {
			l[i] = math.Pow((f+.055)/1.055, 2.4)
		}
	}
	return .2126*l[0] + .7152*l[1] + .0722*l[2]
}
