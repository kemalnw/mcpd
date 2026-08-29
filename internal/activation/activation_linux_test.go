//go:build linux

package activation

import (
	"net"
	"os"
	"testing"
)

func TestListenerForDuplicatesReusableActivatedSocket(t *testing.T) {
	base, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	file, err := base.(*net.TCPListener).File()
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	set := &Set{files: []*os.File{file}}
	address := base.Addr().String()
	first, ok, err := set.ListenerFor(address)
	if err != nil || !ok {
		t.Fatalf("first listener: ok=%v err=%v", ok, err)
	}
	_ = first.Close()
	second, ok, err := set.ListenerFor(address)
	if err != nil || !ok {
		t.Fatalf("second listener: ok=%v err=%v", ok, err)
	}
	_ = second.Close()
}

func TestListenerForReturnsNoMatch(t *testing.T) {
	set := &Set{}
	if listener, ok, err := set.ListenerFor("127.0.0.1:443"); err != nil || ok || listener != nil {
		t.Fatalf("listener=%v ok=%v err=%v", listener, ok, err)
	}
}
func TestListenerForSelectsMatchingPortAcrossMultipleSockets(t *testing.T) {
	one, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer one.Close()
	two, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer two.Close()
	fileOne, _ := one.(*net.TCPListener).File()
	defer fileOne.Close()
	fileTwo, _ := two.(*net.TCPListener).File()
	defer fileTwo.Close()
	set := &Set{files: []*os.File{fileOne, fileTwo}}
	listener, ok, err := set.ListenerFor(two.Addr().String())
	if err != nil || !ok {
		t.Fatalf("listener ok=%v err=%v", ok, err)
	}
	defer listener.Close()
	if listener.Addr().(*net.TCPAddr).Port != two.Addr().(*net.TCPAddr).Port {
		t.Fatalf("selected %v, want %v", listener.Addr(), two.Addr())
	}
}
