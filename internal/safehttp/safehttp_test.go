package safehttp

import (
	"net"
	"testing"
)

func TestBlockedCoversSpecialPurposeRanges(t *testing.T) {
	for _, s := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "100.64.0.1", "198.18.0.1", "0.1.2.3", "240.0.0.1", "64:ff9b::7f00:1", "::ffff:127.0.0.1"} {
		if !blocked(net.ParseIP(s)) {
			t.Errorf("blocked(%s) = false", s)
		}
	}
	if blocked(net.ParseIP("93.184.215.14")) {
		t.Error("a public address is blocked")
	}
}
