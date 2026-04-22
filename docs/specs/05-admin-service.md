# 05. Admin Service - Спецификация

## Описание

Сервис административных функций. Управление пользователями, серверами, обработка заявок на вывод средств, статистика и аналитика.

## Требования

### Функциональные:
- Список пользователей с фильтрами (роль, статус)
- Бан/разбан пользователей
- Изменение роли пользователя
- Список заявок на вывод средств
- Одобрение/отклонение заявок на вывод
- Dashboard статистика (пользователи, подписки, доход)
- Список всех подписок
- Список всех платежей

### Нефункциональные:
- Только для пользователей с ролью `admin`
- Логирование всех административных действий
- Pagination для больших списков

## Схема базы данных

Использует все таблицы для чтения и некоторые для записи:
- `users` - управление пользователями
- `subscriptions` - просмотр подписок
- `payments` - просмотр платежей
- `withdrawal_requests` - обработка заявок
- `vpn_servers` - управление серверами (через VPN Service)

## API (gRPC)

```protobuf
service AdminService {
  // Пользователи
  rpc ListUsers(ListUsersRequest) returns (ListUsersResponse);
  rpc BanUser(BanUserRequest) returns (BanUserResponse);
  rpc UpdateUserRole(UpdateUserRoleRequest) returns (UpdateUserRoleResponse);
  
  // Заявки на вывод
  rpc ListWithdrawalRequests(ListWithdrawalRequestsRequest) returns (ListWithdrawalRequestsResponse);
  rpc ProcessWithdrawal(ProcessWithdrawalRequest) returns (ProcessWithdrawalResponse);
  
  // Статистика
  rpc GetDashboardStats(GetDashboardStatsRequest) returns (GetDashboardStatsResponse);
  
  // Подписки и платежи
  rpc ListSubscriptions(ListSubscriptionsRequest) returns (ListSubscriptionsResponse);
  rpc ListPayments(ListPaymentsRequest) returns (ListPaymentsResponse);
}
```

## План реализации

1. **Структура проекта** (1 час)
2. **Proto определения** (30 минут)
3. **Database Repository** (2 часа)
4. **Business Logic** (3 часа)
   - Фильтрация и пагинация
   - Обработка заявок на вывод
   - Агрегация статистики
5. **gRPC API** (2 часа)
6. **Authorization Middleware** (1 час)
   - Проверка роли admin
7. **Тестирование** (2 часа)
8. **Документация** (30 минут)

**Итого: 12 часов (≈ 1.5 рабочих дня)**

## Dashboard Статистика

```go
type DashboardStats struct {
    TotalUsers int64
    ActiveSubscriptions int64
    TotalRevenue decimal.Decimal
    RevenueThisMonth decimal.Decimal
    NewUsersToday int64
    PendingWithdrawals int64
}
```

## Примеры

### Список пользователей:
```go
resp, err := adminClient.ListUsers(ctx, &admin.ListUsersRequest{
    Role: "partner",
    Page: 1,
    Limit: 50,
})
// resp.Users - список пользователей
// resp.Total - общее количество
```

### Обработка заявки на вывод:
```go
resp, err := adminClient.ProcessWithdrawal(ctx, &admin.ProcessWithdrawalRequest{
    RequestId: 123,
    Status: "approved",
    AdminComment: "Выплачено на карту",
})
// Обновляется статус, уменьшается баланс пользователя
```

### Dashboard статистика:
```go
resp, err := adminClient.GetDashboardStats(ctx, &admin.GetDashboardStatsRequest{})
// resp.TotalUsers, resp.ActiveSubscriptions, resp.TotalRevenue...
```
