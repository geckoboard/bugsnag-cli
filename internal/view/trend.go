package view

// TrendBucket is one bucket from the trend endpoint.
//
// The error object's own `trend` field is only populated when a histogram is
// requested, and the view-error endpoint takes no parameters, so the detail page
// reads the dedicated trend endpoint instead. Its shape is
// {"from","to","events_count"}, not the [date, count] pairs the error's trend
// field uses.
type TrendBucket struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Events int    `json:"events_count"`
}

// bucketCounts extracts the counts for a sparkline.
func bucketCounts(buckets []TrendBucket) []int {
	counts := make([]int, 0, len(buckets))
	for _, b := range buckets {
		counts = append(counts, b.Events)
	}
	return counts
}
