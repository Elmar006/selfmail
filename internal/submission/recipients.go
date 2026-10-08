package submission

import (
	"sort"
	"strings"
)

func joinSorted(s []string) string {
	v := append([]string(nil), s...)
	sort.Strings(v)
	return strings.Join(v, ",")
}
