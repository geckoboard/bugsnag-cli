package view

import "strings"

// sparkBlocks are the eight one-cell block characters, lowest to highest.
var sparkBlocks = []rune{'▁', '▂', '▃', '▄', '▅', '▆', '▇', '█'}

// sparkline renders counts as one character per bucket.
//
// It is emitted piped as well as on a terminal: ten characters for a whole
// series is cheap, and the shape is often the answer on its own.
func sparkline(counts []int) string {
	if len(counts) == 0 {
		return ""
	}

	maxCount := 0
	for _, c := range counts {
		if c > maxCount {
			maxCount = c
		}
	}

	var b strings.Builder
	b.Grow(len(counts) * 3)
	for _, c := range counts {
		// The lowest block is reserved for an empty bucket, which also covers the
		// all-zero series that would otherwise divide by zero.
		if c <= 0 {
			b.WriteRune(sparkBlocks[0])
			continue
		}
		// Round up, so the smallest non-zero count still clears the floor: a
		// bucket with events must never look identical to one without. Truncating
		// here rendered one event against a maximum of a thousand as the empty
		// block.
		idx := (c*(len(sparkBlocks)-1) + maxCount - 1) / maxCount
		b.WriteRune(sparkBlocks[idx])
	}
	return b.String()
}

// sparklineN renders counts as at most n characters, grouping buckets when there
// are more than that. A 30-bucket trend is too wide for a table column, and the
// shape survives grouping.
//
// Every group holds at least one bucket, since this only groups when there are
// more buckets than characters to spend.
func sparklineN(counts []int, n int) string {
	if n <= 0 || len(counts) <= n {
		return sparkline(counts)
	}

	grouped := make([]int, 0, n)
	for i := range n {
		sum := 0
		for _, c := range counts[i*len(counts)/n : (i+1)*len(counts)/n] {
			sum += c
		}
		grouped = append(grouped, sum)
	}
	return sparkline(grouped)
}

// trendStart is the first bucket's date, so a view can say what a sparkline
// actually spans rather than leaving it to be guessed.
func trendStart(trend *[][]any) string {
	if trend == nil || len(*trend) == 0 {
		return ""
	}

	if first := (*trend)[0]; len(first) > 0 {
		if s, ok := first[0].(string); ok {
			return s
		}
	}
	return ""
}

// trendCounts reads the counts out of an ErrorApiView.trend value.
//
// trend is an array of [date, count] pairs. The generated type is
// [][]interface{}, which is correct: the schema's items.items has no type, so
// there is nothing better to generate and it unmarshals fine.
func trendCounts(trend *[][]any) []int {
	if trend == nil {
		return nil
	}

	counts := make([]int, 0, len(*trend))
	for _, pair := range *trend {
		if len(pair) < 2 {
			continue
		}
		// float64 is the only form a count arrives in: the trend is decoded by a
		// plain unmarshal into [][]any.
		if n, ok := pair[1].(float64); ok {
			counts = append(counts, int(n))
		}
	}
	return counts
}
