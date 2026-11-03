package singbox

import (
	"fmt"
	"golang.org/x/sys/unix"
	"strings"
)

func duplicateTun(fd int) (int, string, error) {
	owned, err := unix.FcntlInt(uintptr(fd), unix.F_DUPFD_CLOEXEC, 3)
	if err != nil {
		return -1, "", err
	}
	name, err := unix.GetsockoptString(owned, 2, 2) // SYSPROTO_CONTROL, UTUN_OPT_IFNAME
	if err == nil && !validUTUNName(name) {
		err = fmt.Errorf("descriptor does not identify a utun interface")
	}
	if err == nil {
		err = unix.SetNonblock(owned, true)
	}
	if err != nil {
		unix.Close(owned)
		return -1, "", err
	}
	return owned, name, nil
}
func validUTUNName(name string) bool {
	suffix, ok := strings.CutPrefix(name, "utun")
	if !ok || suffix == "" {
		return false
	}
	for _, digit := range suffix {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	return true
}
func closeDescriptor(fd int) { _ = unix.Close(fd) }
