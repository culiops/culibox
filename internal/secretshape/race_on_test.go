//go:build race

package secretshape

// slowFactor scales the wall-clock bound of the speed test: the race detector
// makes the same work an order of magnitude slower.
const slowFactor = 15
