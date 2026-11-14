package netguard

import (
	"net/netip"
	"testing"
)

func BenchmarkPublic(b *testing.B) {
	addrs := []netip.Addr{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("10.0.0.1"),
		netip.MustParseAddr("2606:4700::1111"), netip.MustParseAddr("100.64.0.1")}
	b.ReportAllocs()
	for b.Loop() {
		for _, a := range addrs {
			_ = Public(a)
		}
	}
}
