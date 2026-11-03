package singbox

import "golang.org/x/sys/unix"

func duplicateTun(fd int) (int, string, error) {
	owned, err := unix.FcntlInt(uintptr(fd), unix.F_DUPFD_CLOEXEC, 3)
	if err != nil {
		return -1, "", err
	}
	request, err := unix.NewIfreq("")
	if err == nil {
		err = unix.IoctlIfreq(owned, unix.TUNGETIFF, request)
	}
	if err != nil {
		unix.Close(owned)
		return -1, "", err
	}
	if err = unix.SetNonblock(owned, true); err != nil {
		unix.Close(owned)
		return -1, "", err
	}
	return owned, request.Name(), nil
}
func closeDescriptor(fd int) { _ = unix.Close(fd) }
