package xray

import (
	"context"
	"fmt"
	"sync"
)

// Pool — thread-safe пул Xray gRPC-клиентов, по одному на каждый сервер
// из таблицы vpn_servers (key = server_id).
//
// Подключения lazy: создаются при первом GetOrConnect и переиспользуются.
// gRPC ClientConn внутри сам переподключается при сетевых сбоях, поэтому
// закэшированный клиент остаётся валиден между запросами.
//
// Для multi-server: vpn-service пересмотрев список активных серверов в БД,
// для каждого делает pool.GetOrConnect(srv.ID, addr) перед AddUser/RemoveUser.
// Если новый сервер появился в БД — pool подцепит его автоматически при
// следующем запросе. Если сервер убрали (is_active=false) — клиент остаётся
// в кэше до явного Remove или Close, но в цикл по серверам он уже не попадёт
// (т.к. ListServers(active=true) его не вернёт).
type Pool struct {
	mu      sync.RWMutex
	clients map[int32]*Client
}

// NewPool создаёт пустой пул. Подключения создаются лениво.
func NewPool() *Pool {
	return &Pool{clients: make(map[int32]*Client)}
}

// GetOrConnect возвращает клиент для serverID. Если его нет в кэше — создаёт
// новое подключение к addr (формат "host:port") и сохраняет.
//
// Ошибка возникает только при невозможности установить gRPC-соединение
// (xray.New блокируется до 5с). Дальнейшие AddUser/RemoveUser могут
// упасть отдельно — это уже обрабатывается на уровне VPNService.
func (p *Pool) GetOrConnect(ctx context.Context, serverID int32, addr string) (*Client, error) {
	p.mu.RLock()
	if c, ok := p.clients[serverID]; ok {
		p.mu.RUnlock()
		return c, nil
	}
	p.mu.RUnlock()

	p.mu.Lock()
	defer p.mu.Unlock()
	// Double-check после захвата write-lock — другая горутина могла создать.
	if c, ok := p.clients[serverID]; ok {
		return c, nil
	}
	cli, err := New(ctx, addr)
	if err != nil {
		return nil, fmt.Errorf("xray pool: connect server_id=%d addr=%s: %w", serverID, addr, err)
	}
	p.clients[serverID] = cli
	return cli, nil
}

// Get возвращает уже подключённый клиент или nil без попытки коннекта.
// Полезно для read-only сценариев (heartbeat, debug).
func (p *Pool) Get(serverID int32) *Client {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.clients[serverID]
}

// Remove закрывает соединение и удаляет клиент из пула. Используется при
// is_active=false → drop-у сервера из конфигурации.
func (p *Pool) Remove(serverID int32) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if c, ok := p.clients[serverID]; ok {
		_ = c.Close()
		delete(p.clients, serverID)
	}
}

// Close закрывает все соединения. Вызывается при шатдауне приложения.
func (p *Pool) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, c := range p.clients {
		_ = c.Close()
	}
	p.clients = nil
	return nil
}
