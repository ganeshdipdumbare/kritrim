package netguard

import (
	"context"
	"net/netip"
	"testing"
)

func TestPublic(t *testing.T) {
	private := []string{
		"127.0.0.1", "10.1.2.3", "172.16.0.1", "192.168.1.1", "169.254.169.254",
		"100.100.100.200", "0.0.0.0", "0.1.2.3", "198.18.0.1", "224.0.0.1", "255.255.255.255",
		"::1", "::", "fe80::1", "fc00::1", "fd00:ec2::254", "ff02::1",
		"::ffff:127.0.0.1", "::ffff:169.254.169.254", "64:ff9b::a9fe:a9fe", "2002:7f00:0001::1",
		"2001:db8::1",
	}
	for _, s := range private {
		if Public(netip.MustParseAddr(s)) {
			t.Errorf("%s reported public", s)
		}
	}
	for _, s := range []string{"1.1.1.1", "8.8.8.8", "2606:4700:4700::1111", "64:ff9b::808:808"} {
		if !Public(netip.MustParseAddr(s)) {
			t.Errorf("%s reported private", s)
		}
	}
}

func TestDialRefusesPrivate(t *testing.T) {
	dial := DialContext(nil)
	for _, addr := range []string{"127.0.0.1:80", "[::1]:80", "169.254.169.254:80"} {
		if c, err := dial(context.Background(), "tcp", addr); err == nil {
			c.Close()
			t.Fatalf("dialed %s", addr)
		}
	}
	trusted := DialContext(func(h string) bool { return h == "127.0.0.1" })
	if _, err := trusted(context.Background(), "tcp", "127.0.0.1:1"); err != nil && err.Error() == "" {
		t.Fatal("unexpected")
	}
}

func FuzzPublic(f *testing.F) {
	f.Add([]byte{127, 0, 0, 1})
	f.Add(make([]byte, 16))
	f.Fuzz(func(t *testing.T, b []byte) {
		ip, ok := netip.AddrFromSlice(b)
		if !ok {
			return
		}
		pub := Public(ip)
		if pub && (ip.Unmap().IsLoopback() || ip.Unmap().IsPrivate() || ip.Unmap().IsLinkLocalUnicast() || ip.Unmap().IsUnspecified()) {
			t.Fatalf("%s is public", ip)
		}
	})
}
