//go:build race

package clone

// slowFactor scales the wall-clock bounds of the cost tests: the race detector
// makes the same work an order of magnitude slower.
const slowFactor = 15
