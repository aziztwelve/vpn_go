package handler

import (
	"strings"
	"testing"
	"time"

	broadcastpb "github.com/vpn/shared/pkg/proto/broadcast/v1"
)

// TestFormatRelTime — относительное время, должно правильно
// классифицировать минуты/часы/дни. Граничные случаи: 0 → "—",
// >7 дней → дата.
func TestFormatRelTime(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name     string
		unix     int64
		contains string
	}{
		{"zero is dash", 0, "—"},
		{"30s ago is just-now", now.Add(-30 * time.Second).Unix(), "только что"},
		{"5m ago", now.Add(-5 * time.Minute).Unix(), "5м назад"},
		{"2h ago", now.Add(-2 * time.Hour).Unix(), "2ч назад"},
		{"3d ago", now.Add(-3 * 24 * time.Hour).Unix(), "3д назад"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := formatRelTime(c.unix)
			if got != c.contains {
				t.Errorf("formatRelTime(%d) = %q, want %q", c.unix, got, c.contains)
			}
		})
	}

	// > 7 days → формат "DD.MM"
	old := now.Add(-30 * 24 * time.Hour).Unix()
	got := formatRelTime(old)
	if len(got) != 5 || got[2] != '.' {
		t.Errorf("formatRelTime(>7d) = %q, expected DD.MM format", got)
	}
}

// TestStatusEmoji — все известные status'ы должны иметь
// уникальный визуальный маркер; неизвестный — fallback "❓".
func TestStatusEmoji(t *testing.T) {
	known := []string{"draft", "approved", "sending", "sent", "cancelled", "failed"}
	seen := map[string]string{}
	for _, s := range known {
		e := statusEmoji(s)
		if e == "❓" {
			t.Errorf("status %q got fallback emoji", s)
		}
		if prev, ok := seen[e]; ok {
			t.Errorf("status %q and %q share emoji %q", prev, s, e)
		}
		seen[e] = s
	}
	if statusEmoji("garbage") != "❓" {
		t.Error("unknown status should fallback to ❓")
	}
}

// TestFormatAdminList_Pending — pending-блок должен содержать
// /approve_<id>, /cancel_<id>, /broadcast_stats_<id> для каждого
// драфта (это якорь для формата сообщения — клиенты Telegram
// автоматически делают такие токены кликабельными).
func TestFormatAdminList_Pending(t *testing.T) {
	now := time.Now().Unix()
	pending := []*broadcastpb.DraftSummary{
		{Id: 42, SegmentKey: "trial_never_connected", RecipientCount: 14, Status: "draft", CreatedAtUnix: now - 3600},
		{Id: 43, SegmentKey: "trial_ending_idle", RecipientCount: 7, Status: "draft", CreatedAtUnix: now - 7200},
	}
	out := formatAdminList(pending, nil)

	for _, want := range []string{
		"Pending (2)",
		"#42 trial_never_connected",
		"#43 trial_ending_idle",
		"/approve_42",
		"/cancel_42",
		"/broadcast_stats_42",
		"/approve_43",
		"/cancel_43",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("formatAdminList missing %q\n--- got ---\n%s", want, out)
		}
	}
}

// TestFormatAdminList_Empty — без pending должна быть строка про
// отсутствие; recent-блок если задан — выводится.
func TestFormatAdminList_Empty(t *testing.T) {
	out := formatAdminList(nil, nil)
	if !strings.Contains(out, "Нет pending драфтов") {
		t.Errorf("expected empty marker, got: %s", out)
	}

	now := time.Now().Unix()
	recent := []*broadcastpb.DraftSummary{
		{Id: 10, SegmentKey: "trial_ending_active", RecipientCount: 3, Status: "sent", CreatedAtUnix: now - 86400},
	}
	out = formatAdminList(nil, recent)
	if !strings.Contains(out, "Последние:") || !strings.Contains(out, "#10") {
		t.Errorf("expected recent block, got: %s", out)
	}
}

// TestFormatBroadcastDetails — все ключевые поля должны попасть в
// форматированный вывод. Для status='draft' добавляются action-команды.
func TestFormatBroadcastDetails(t *testing.T) {
	now := time.Now().Unix()
	d := &broadcastpb.BroadcastDetails{
		Id:               42,
		SegmentKey:       "trial_never_connected",
		Title:            "Onboarding",
		BodyTemplate:     "Hello {{first_name}}",
		RecipientCount:   14,
		Status:           "sending",
		CreatedAtUnix:    now - 3600,
		ApprovedAtUnix:   now - 1800,
		ApprovedByUserId: 5,
		Stats: &broadcastpb.Stats{
			Sent: 12, Blocked: 1, Failed: 0, Opened: 4, Clicked: 1,
		},
	}
	out := formatBroadcastDetails(d)

	for _, want := range []string{
		"Broadcast #42",
		"sending",
		"trial_never_connected",
		"Onboarding",
		"Recipients: 14",
		"sent: 12",
		"blocked: 1",
		"opened: 4",
		"clicked: 1",
		"admin user_id=5",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("formatBroadcastDetails missing %q\n--- got ---\n%s", want, out)
		}
	}

	// /approve и /cancel должны появляться только для draft.
	if strings.Contains(out, "/approve_42") {
		t.Error("non-draft status must not show /approve action")
	}

	d.Status = "draft"
	d.ApprovedAtUnix = 0
	out = formatBroadcastDetails(d)
	if !strings.Contains(out, "/approve_42") || !strings.Contains(out, "/cancel_42") {
		t.Errorf("draft status must show actions, got:\n%s", out)
	}
}
