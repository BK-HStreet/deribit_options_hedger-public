// File: internal/strategy/helpers.go
package strategy

// small math helpers used across strategy package (put common helpers here)
// keep these minimal and efficient to avoid perf/regressions in HFT code.

func minf(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func maxf(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func min3(a, b, c float64) float64 {
	if a < b {
		if a < c {
			return a
		}
		return c
	}
	if b < c {
		return b
	}
	return c
}

func absf(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}
