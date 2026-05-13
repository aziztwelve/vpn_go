package service

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/vpn/referral-service/internal/model"
	"github.com/vpn/referral-service/internal/repository"
	"go.uber.org/zap"
)

// CampaignService — управление маркетинговыми воронками (CRUD + статистика).
//
// Расположен рядом с Referral-сервисом, потому что:
//   1. Шарят БД (одна Postgres) и ходят в одни и те же таблицы (users.balance).
//   2. Партнёрские выплаты по кампании тоже бегут через ApplyBonus в referral.go.
//   3. Не плодим лишний микросервис — gRPC-сервер регистрируется в том же
//      бинарнике/порту.
type CampaignService struct {
	repo *repository.Repository
	cfg  CampaignConfig
	log  *zap.Logger
}

// CampaignConfig — настройки билда deep-link'а. Шарим BotUsername с ReferralConfig
// чтобы один бот = один URL.
type CampaignConfig struct {
	BotUsername string
}

// CampaignSrcStartPrefix — префикс start-параметра для deep-link'ов кампаний.
// Чётко изолирован от ref_<token> чтобы парсер бота не путался.
const CampaignSrcStartPrefix = "src_"

// MaxPayoutPercent — потолок процента выплат партнёру по кампании.
// Совпадает с ограничением в БД (CHECK payout_percent BETWEEN 0 AND 50).
const MaxPayoutPercent = 50

var slugRe = regexp.MustCompile(model.CampaignSlugRegex)

// ErrInvalidSlug — slug не прошёл валидацию.
var ErrInvalidSlug = errors.New("invalid campaign slug (lower-case [a-z0-9_-]{3,60})")

// ErrInvalidPayoutPercent — процент вне допустимого диапазона.
var ErrInvalidPayoutPercent = fmt.Errorf("payout_percent must be in [0, %d]", MaxPayoutPercent)

// ErrPayoutWithoutPartner — нельзя задать процент без партнёра-получателя.
var ErrPayoutWithoutPartner = errors.New("payout_percent requires partner_user_id")

// ErrInvalidTrialDuration — override триала вне набора пресетов.
// Совпадает с CHECK в миграции 004_campaign_trial_override.up.sql.
var ErrInvalidTrialDuration = fmt.Errorf("trial_duration_days must be one of %v or null", model.AllowedTrialDurationDays)

// NewCampaign — конструктор сервиса.
func NewCampaign(repo *repository.Repository, cfg CampaignConfig, log *zap.Logger) *CampaignService {
	return &CampaignService{repo: repo, cfg: cfg, log: log}
}

// CreateCampaignInput — параметры из gRPC API.
//   PartnerUserID == 0     → без партнёра
//   PayoutPercent == 0     → без выплат
//   TrialDurationDays == 0 → без override (дефолт 3 дня)
type CreateCampaignInput struct {
	Slug              string
	Name              string
	Notes             string
	PartnerUserID     int64
	PayoutPercent     int32
	CreatedBy         int64
	TrialDurationDays int32
}

// Create — валидация + INSERT.
func (s *CampaignService) Create(ctx context.Context, in CreateCampaignInput) (*model.Campaign, error) {
	if !slugRe.MatchString(in.Slug) {
		return nil, ErrInvalidSlug
	}
	if in.Name == "" {
		return nil, errors.New("name is required")
	}
	if in.CreatedBy <= 0 {
		return nil, errors.New("created_by is required")
	}
	if in.PayoutPercent < 0 || in.PayoutPercent > MaxPayoutPercent {
		return nil, ErrInvalidPayoutPercent
	}
	if in.PayoutPercent > 0 && in.PartnerUserID == 0 {
		return nil, ErrPayoutWithoutPartner
	}
	// trial_duration_days: 0 = без override; иначе должен быть в списке пресетов.
	var trialPtr *int32
	if in.TrialDurationDays != 0 {
		v := in.TrialDurationDays
		if !model.IsValidTrialDurationDays(&v) {
			return nil, ErrInvalidTrialDuration
		}
		trialPtr = &v
	}

	c := &model.Campaign{
		Slug:              in.Slug,
		Name:              in.Name,
		Notes:             in.Notes,
		CreatedBy:         in.CreatedBy,
		TrialDurationDays: trialPtr,
	}
	if in.PartnerUserID > 0 {
		c.PartnerUserID = &in.PartnerUserID
	}
	if in.PayoutPercent > 0 {
		c.PayoutPercent = &in.PayoutPercent
	}

	if err := s.repo.CreateCampaign(ctx, c); err != nil {
		return nil, err
	}
	s.log.Info("campaign created",
		zap.Int64("id", c.ID),
		zap.String("slug", c.Slug),
		zap.Int64("created_by", c.CreatedBy),
	)
	return c, nil
}

// UpdateInput — частичное обновление. nil = "не менять".
//   ClearPartner=true        → обнуляет partner_user_id (использовать когда хотим
//                              убрать выплаты совсем, а не "оставить как было").
//   ClearTrialDuration=true  → возвращает дефолт 3 дня (NULL в БД).
type UpdateCampaignInput struct {
	Name               *string
	Notes              *string
	PartnerUserID      *int64
	PayoutPercent      *int32
	ClearPartner       bool
	ClearPayout        bool
	TrialDurationDays  *int32
	ClearTrialDuration bool
}

func (s *CampaignService) Update(ctx context.Context, id int64, in UpdateCampaignInput) (*model.Campaign, error) {
	if id <= 0 {
		return nil, errors.New("invalid id")
	}
	if in.PayoutPercent != nil {
		p := *in.PayoutPercent
		if p < 0 || p > MaxPayoutPercent {
			return nil, ErrInvalidPayoutPercent
		}
	}
	// trial-override: либо явное обнуление, либо валидный пресет, либо nil (не менять)
	if !in.ClearTrialDuration && in.TrialDurationDays != nil {
		if !model.IsValidTrialDurationDays(in.TrialDurationDays) {
			return nil, ErrInvalidTrialDuration
		}
	}

	if err := s.repo.UpdateCampaign(ctx, id,
		in.Name, in.Notes,
		in.PartnerUserID, in.PayoutPercent,
		in.ClearPartner, in.ClearPayout,
		in.TrialDurationDays, in.ClearTrialDuration,
	); err != nil {
		return nil, err
	}

	updated, err := s.repo.GetCampaignByID(ctx, id)
	if err != nil {
		return nil, err
	}

	// Дополнительная защита уже после UPDATE: если в результате остался
	// payout_percent, но partner = NULL — это нарушение CHECK constraint,
	// но БД уже бы упала. Проверка — best-effort на случай race condition'а.
	if updated.PayoutPercent != nil && updated.PartnerUserID == nil {
		return nil, ErrPayoutWithoutPartner
	}
	return updated, nil
}

// Archive — soft-delete.
func (s *CampaignService) Archive(ctx context.Context, id int64) (*model.Campaign, error) {
	if id <= 0 {
		return nil, errors.New("invalid id")
	}
	if err := s.repo.ArchiveCampaign(ctx, id); err != nil {
		return nil, err
	}
	return s.repo.GetCampaignByID(ctx, id)
}

// Get — кампания + статистика за период (period может быть нулевым).
func (s *CampaignService) Get(ctx context.Context, id int64, from, to time.Time) (*model.Campaign, *model.CampaignStats, error) {
	if id <= 0 {
		return nil, nil, errors.New("invalid id")
	}
	c, err := s.repo.GetCampaignByID(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	st, err := s.repo.GetCampaignStats(ctx, id, from, to)
	if err != nil {
		return nil, nil, err
	}
	return c, st, nil
}

// List — список с базовой статистикой (за всё время) для каждой.
// Для производительности (N+1 запросы) — на сотнях кампаний приемлемо;
// при росте — рассмотреть один большой JOIN или matview.
func (s *CampaignService) List(ctx context.Context, includeArchived bool, limit, offset int32) ([]*model.Campaign, []*model.CampaignStats, int32, error) {
	items, total, err := s.repo.ListCampaigns(ctx, includeArchived, limit, offset)
	if err != nil {
		return nil, nil, 0, err
	}
	stats := make([]*model.CampaignStats, 0, len(items))
	for _, c := range items {
		st, err := s.repo.GetCampaignStats(ctx, c.ID, time.Time{}, time.Time{})
		if err != nil {
			s.log.Error("get stats for list failed", zap.Int64("campaign_id", c.ID), zap.Error(err))
			// Не валим список из-за статов — возвращаем пустую запись.
			st = &model.CampaignStats{CampaignID: c.ID}
		}
		stats = append(stats, st)
	}
	return items, stats, total, nil
}

// GetStats — отдельный endpoint для детальной страницы (с фильтром периода).
func (s *CampaignService) GetStats(ctx context.Context, id int64, from, to time.Time) (*model.CampaignStats, error) {
	if id <= 0 {
		return nil, errors.New("invalid id")
	}
	return s.repo.GetCampaignStats(ctx, id, from, to)
}

// BuildDeepLink — единая точка построения URL вида:
//   https://t.me/<bot>?start=src_<slug>
// Использовать классический ?start= (а не ?startapp=) по тем же причинам что
// и в реферальной программе — startapp требует main mini app в @BotFather.
func (s *CampaignService) BuildDeepLink(slug string) string {
	return fmt.Sprintf("https://t.me/%s?start=%s%s", s.cfg.BotUsername, CampaignSrcStartPrefix, slug)
}
