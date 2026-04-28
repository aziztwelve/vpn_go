// Package xray — тонкий клиент над Xray gRPC API (HandlerService + StatsService).
//
// Xray экспонирует gRPC API через inbound с `protocol: dokodemo-door`,
// у которого в `settings.address` стоит loopback, а в `api.services` указаны
// нужные сервисы (HandlerService, StatsService). Подключение обычное TCP
// gRPC без TLS — т.к. это внутренний интерфейс.
//
// Мы намеренно импортируем только сгенерированные proto-типы из xray-core
// (`app/proxyman/command`, `app/stats/command`, `common/protocol`,
// `common/serial`, `proxy/vless`), чтобы не тянуть весь рантайм xray.
package xray

import (
	"context"
	"fmt"
	"strings"
	"time"

	command "github.com/xtls/xray-core/app/proxyman/command"
	statscmd "github.com/xtls/xray-core/app/stats/command"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/serial"
	vless "github.com/xtls/xray-core/proxy/vless"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// Client — клиент Xray gRPC API. Потокобезопасен, используется одним
// экземпляром на весь процесс VPN Service.
type Client struct {
	conn    *grpc.ClientConn
	handler command.HandlerServiceClient
	stats   statscmd.StatsServiceClient
}

// New открывает соединение с Xray API. addr — "host:port", например
// "xray:10085" внутри docker-сети или "localhost:10085" локально.
func New(ctx context.Context, addr string) (*Client, error) {
	dialCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	conn, err := grpc.DialContext(dialCtx, addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
	)
	if err != nil {
		return nil, fmt.Errorf("dial xray api at %s: %w", addr, err)
	}
	return &Client{
		conn:    conn,
		handler: command.NewHandlerServiceClient(conn),
		stats:   statscmd.NewStatsServiceClient(conn),
	}, nil
}

// Close закрывает подключение. Безопасно вызывать один раз при шатдауне.
func (c *Client) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

// AddUser добавляет VLESS+XTLS-пользователя в inbound по тегу.
//
//   - inboundTag — имя inbound'а из config.json Xray (напр. "vless-reality-in").
//   - userUUID   — UUID (строка) нового клиента.
//   - email      — email-тег, используется для статистики и для
//     последующего удаления (RemoveUser).
//   - flow       — "xtls-rprx-vision" для Reality; "" для plain VLESS.
//
// Если пользователь уже существует — Xray вернёт ошибку с сообщением вида
// "already exists". Вызывающий код должен это обработать как идемпотентный
// no-op, если требуется.
func (c *Client) AddUser(ctx context.Context, inboundTag, userUUID, email, flow string) error {
	_, err := c.handler.AlterInbound(ctx, &command.AlterInboundRequest{
		Tag: inboundTag,
		Operation: serial.ToTypedMessage(&command.AddUserOperation{
			User: &protocol.User{
				Level: 0,
				Email: email,
				Account: serial.ToTypedMessage(&vless.Account{
					Id:   userUUID,
					Flow: flow,
				}),
			},
		}),
	})
	if err != nil {
		return fmt.Errorf("xray AddUser(tag=%q, email=%q): %w", inboundTag, email, err)
	}
	return nil
}

// RemoveUser удаляет пользователя из inbound по email-тегу (он уникален
// внутри inbound'а).
//
// Если юзера нет — Xray вернёт ошибку "not found"; вызывающему коду
// удобно считать это успехом для идемпотентности.
func (c *Client) RemoveUser(ctx context.Context, inboundTag, email string) error {
	_, err := c.handler.AlterInbound(ctx, &command.AlterInboundRequest{
		Tag: inboundTag,
		Operation: serial.ToTypedMessage(&command.RemoveUserOperation{
			Email: email,
		}),
	})
	if err != nil {
		return fmt.Errorf("xray RemoveUser(tag=%q, email=%q): %w", inboundTag, email, err)
	}
	return nil
}

// Ping — health-check Xray gRPC API. Делает дешёвый QueryStats с
// несовпадающим pattern'ом — Xray возвращает пустой список stats без ошибки,
// если соединение живо. Если контейнер xray умер / TCP оборвался — gRPC
// вернёт error.
//
// Используется ResyncCron для детекции рестартов Xray (он хранит юзеров
// in-memory; после рестарта clients[] обнуляется и нужен ResyncServer).
func (c *Client) Ping(ctx context.Context) error {
	_, err := c.stats.QueryStats(ctx, &statscmd.QueryStatsRequest{
		Pattern: "_xray_health_check_no_match_",
		Reset_:  false,
	})
	if err != nil {
		return fmt.Errorf("xray ping: %w", err)
	}
	return nil
}

// UserStats — трафик одного пользователя в байтах (с момента создания
// или с момента последнего reset'а, если вызывали GetUserStats(reset=true)).
type UserStats struct {
	Uplink   int64
	Downlink int64
}

// GetUserStats возвращает счётчики трафика пользователя по его email-тегу.
// Если reset=true — счётчики обнуляются после чтения (удобно для
// инкрементальных отчётов).
//
// Xray хранит статистику по имени вида "user>>>email>>>traffic>>>{uplink,downlink}".
// Если статистики нет (пользователь только что создан и не передал данных) —
// возвращаем нули без ошибки.
func (c *Client) GetUserStats(ctx context.Context, email string, reset bool) (UserStats, error) {
	resp, err := c.stats.QueryStats(ctx, &statscmd.QueryStatsRequest{
		Pattern: fmt.Sprintf("user>>>%s>>>traffic", email),
		Reset_:  reset,
	})
	if err != nil {
		return UserStats{}, fmt.Errorf("xray QueryStats(email=%q): %w", email, err)
	}

	var out UserStats
	for _, s := range resp.GetStat() {
		switch {
		case strings.HasSuffix(s.Name, "uplink"):
			out.Uplink = s.Value
		case strings.HasSuffix(s.Name, "downlink"):
			out.Downlink = s.Value
		}
	}
	return out, nil
}
