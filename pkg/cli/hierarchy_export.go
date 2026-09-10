package cli

// NormalizeHierarchy is the daemon's view of parseHierarchy: the cross-driver
// tree the `hierarchy` command prints, so GET …/hierarchy returns the same
// shape for every platform instead of Android's raw XML as a string.
func NormalizeHierarchy(raw []byte) (any, error) {
	return parseHierarchy(raw)
}
