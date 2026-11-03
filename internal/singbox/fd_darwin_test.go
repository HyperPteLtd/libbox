package singbox

import "testing"

func TestUTUNNamesAreStrict(t *testing.T) {
	for _, name := range []string{"utun0", "utun17"} {
		if !validUTUNName(name) {
			t.Fatal(name)
		}
	}
	for _, name := range []string{"", "utun", "utun-1", "utun+1", "utunx", "en0", "utun١"} {
		if validUTUNName(name) {
			t.Fatal("accepted", name)
		}
	}
}
