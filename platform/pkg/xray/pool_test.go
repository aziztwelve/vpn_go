package xray

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
)

// startFakeXrayServer — стартует пустой gRPC-сервер на localhost (random port)
// чтобы xray.New(...) успешно установил коннект (он использует grpc.WithBlock()
// и блокируется до HTTP/2-handshake'а; пустой grpc.Server с этим успешно
// справляется, реальные RPC-вызовы наш Pool в этих тестах не делает).
func startFakeXrayServer(t *testing.T) (addr string, stop func()) {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := grpc.NewServer()
	go srv.Serve(lis)
	return lis.Addr().String(), func() {
		srv.GracefulStop()
		_ = lis.Close()
	}
}

func TestPool_GetOrConnect_CachesByServerID(t *testing.T) {
	addr, stop := startFakeXrayServer(t)
	defer stop()

	p := NewPool()
	defer p.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c1, err := p.GetOrConnect(ctx, 1, addr)
	if err != nil {
		t.Fatalf("first GetOrConnect: %v", err)
	}
	c2, err := p.GetOrConnect(ctx, 1, addr)
	if err != nil {
		t.Fatalf("second GetOrConnect: %v", err)
	}
	if c1 != c2 {
		t.Errorf("expected same cached client for server_id=1, got different instances")
	}
}

func TestPool_GetOrConnect_DifferentServerIDs(t *testing.T) {
	addr1, stop1 := startFakeXrayServer(t)
	defer stop1()
	addr2, stop2 := startFakeXrayServer(t)
	defer stop2()

	p := NewPool()
	defer p.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c1, err := p.GetOrConnect(ctx, 1, addr1)
	if err != nil {
		t.Fatalf("server_id=1: %v", err)
	}
	c2, err := p.GetOrConnect(ctx, 2, addr2)
	if err != nil {
		t.Fatalf("server_id=2: %v", err)
	}
	if c1 == c2 {
		t.Errorf("expected different clients for different server_ids, got same instance")
	}
}

func TestPool_Get_ReturnsNilForUnknownServer(t *testing.T) {
	p := NewPool()
	defer p.Close()
	if got := p.Get(42); got != nil {
		t.Errorf("expected nil for unknown server_id, got %v", got)
	}
}

func TestPool_Get_ReturnsCachedClient(t *testing.T) {
	addr, stop := startFakeXrayServer(t)
	defer stop()

	p := NewPool()
	defer p.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	want, err := p.GetOrConnect(ctx, 7, addr)
	if err != nil {
		t.Fatalf("GetOrConnect: %v", err)
	}
	got := p.Get(7)
	if got != want {
		t.Errorf("Get returned different client than GetOrConnect cached")
	}
}

func TestPool_Remove_DropsClient(t *testing.T) {
	addr, stop := startFakeXrayServer(t)
	defer stop()

	p := NewPool()
	defer p.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := p.GetOrConnect(ctx, 3, addr); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if p.Get(3) == nil {
		t.Fatalf("client should be in pool after GetOrConnect")
	}
	p.Remove(3)
	if p.Get(3) != nil {
		t.Errorf("Get(3) after Remove should return nil")
	}
}

func TestPool_Close_AllowsRepeatedCalls(t *testing.T) {
	p := NewPool()
	if err := p.Close(); err != nil {
		t.Errorf("first Close: %v", err)
	}
	// Повторный Close не должен паниковать (clients == nil).
	if err := p.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

func TestPool_GetOrConnect_BadAddrReturnsError(t *testing.T) {
	p := NewPool()
	defer p.Close()

	// Очень короткий таймаут чтобы тест не висел: dial-in xray.New использует
	// 5с по умолчанию, а нам нужен fail-fast на нерезолвящийся адрес.
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	_, err := p.GetOrConnect(ctx, 999, "127.0.0.1:1")
	if err == nil {
		t.Errorf("expected error for unreachable addr, got nil")
	}
	if got := p.Get(999); got != nil {
		t.Errorf("failed connect must NOT cache client; Get returned %v", got)
	}
}
