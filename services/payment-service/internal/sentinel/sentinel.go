// Package sentinel — фоновый воркер, который добивает «зависшие» платежи.
//
// Сценарий: webhook от провайдера прошёл через шаг 1 (MarkPaidDBOnly), но
// между шагами 2/3 что-то упало (subscription-service лёг, vpn-service
// недоступен, контейнер рестартанул). Платёж застрял в промежуточном
// статусе paid_db_only / paid_subscription_done.
//
// Sentinel раз в Interval сканирует таблицу payments на такие зависшие
// записи (старше StaleAfter) и для каждой повторно вызывает
// service.handleSuccessfulPayment. Поскольку state machine идемпотентен —
// уже выполненные шаги пропускаются, и flow продолжается с того места,
// где остановился. См. service.handleSuccessfulPayment.
package sentinel

import (
	"context"
	"sync"
	"time"

	"github.com/vpn/payment-service/internal/model"
	"github.com/vpn/payment-service/internal/repository"
	"go.uber.org/zap"
)

// PaymentResumer — минимальный contract для resume логики. Реализуется
// в service.PaymentService.
type PaymentResumer interface {
	// ResumePaid повторно прогоняет state machine для зависшего платежа.
	// Должен быть идемпотентным: если шаг уже выполнен — пропустить.
	ResumePaid(ctx context.Context, payment *model.Payment) error
}

// Config — настройки sentinel'а.
type Config struct {
	// Interval — как часто сканировать таблицу. По умолчанию 60s.
	Interval time.Duration
	// StaleAfter — какой минимальный возраст зависшего платежа считается
	// «застрявшим». Должен быть больше типичного времени retry'я webhook'ов
	// провайдера, иначе будем добивать одновременно с ретраями. По умолчанию 5m.
	StaleAfter time.Duration
	// BatchLimit — сколько платежей за один тик обрабатывать максимум.
	// Защита от резкого скачка. По умолчанию 50.
	BatchLimit int32
}

// Sentinel — воркер, запускается через Start, останавливается отменой контекста.
type Sentinel struct {
	repo    *repository.PaymentRepository
	resumer PaymentResumer
	cfg     Config
	log     *zap.Logger
}

// New создаёт sentinel с дефолтами для пустых полей конфига.
func New(repo *repository.PaymentRepository, resumer PaymentResumer, cfg Config, log *zap.Logger) *Sentinel {
	if cfg.Interval == 0 {
		cfg.Interval = 60 * time.Second
	}
	if cfg.StaleAfter == 0 {
		cfg.StaleAfter = 5 * time.Minute
	}
	if cfg.BatchLimit == 0 {
		cfg.BatchLimit = 50
	}
	return &Sentinel{repo: repo, resumer: resumer, cfg: cfg, log: log}
}

// Start запускает фоновый цикл. Блокируется до отмены ctx.
// Один тик сразу при старте — чтобы быстро добить то, что зависло во время
// рестарта сервиса.
func (s *Sentinel) Start(ctx context.Context, wg *sync.WaitGroup) {
	if wg != nil {
		wg.Add(1)
		defer wg.Done()
	}

	s.log.Info("payment sentinel started",
		zap.Duration("interval", s.cfg.Interval),
		zap.Duration("stale_after", s.cfg.StaleAfter),
		zap.Int32("batch_limit", s.cfg.BatchLimit),
	)

	// Первый тик — сразу.
	s.tick(ctx)

	t := time.NewTicker(s.cfg.Interval)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			s.log.Info("payment sentinel stopped")
			return
		case <-t.C:
			s.tick(ctx)
		}
	}
}

// tick — один цикл проверки и ресюма.
func (s *Sentinel) tick(ctx context.Context) {
	stuck, err := s.repo.ListStuckPaid(ctx, s.cfg.StaleAfter, s.cfg.BatchLimit)
	if err != nil {
		s.log.Error("sentinel: list stuck failed", zap.Error(err))
		return
	}
	if len(stuck) == 0 {
		return
	}

	s.log.Info("sentinel: found stuck payments", zap.Int("count", len(stuck)))

	for _, p := range stuck {
		// Используем фоновый ctx с таймаутом — не привязываемся к Start ctx,
		// чтобы внезапная отмена tick'а не оборвала ресюм посередине шага.
		// Если что — следующий тик через минуту добьёт.
		resumeCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		err := s.resumer.ResumePaid(resumeCtx, p)
		cancel()
		if err != nil {
			s.log.Warn("sentinel: resume failed (will retry next tick)",
				zap.Int64("payment_id", p.ID),
				zap.String("status", p.Status),
				zap.Error(err),
			)
			continue
		}
		s.log.Info("sentinel: resumed payment",
			zap.Int64("payment_id", p.ID),
			zap.String("from_status", p.Status),
		)
	}
}


