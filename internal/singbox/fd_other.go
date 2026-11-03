//go:build !darwin && !linux

package singbox

func duplicateTun(int) (int, string, error) { return -1, "", unsupported() }
func closeDescriptor(int)                   {}
