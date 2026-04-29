package service

import (
	"strings"
)

// NormalizeUserAgent превращает сырой User-Agent в короткий device_identifier
// для UI: "Happ / iOS", "v2RayNG / Android", "Hiddify / Windows", "Streisand / iOS"
// и т.п. Намеренно грубо — мы НЕ парсим вычурные UA и НЕ различаем версии.
//
// Мотивация: при subscription-flow клиент-приложение тянет URL со своим UA,
// и мы хотим хоть как-то идентифицировать устройство для /devices страницы.
// Точная биометрия девайса невозможна (UUID один на всех), поэтому UA — это
// best-effort, перекрывающий типовой кейс «iPhone в Happ + Mac в Streisand».
//
// Если UA пустой/неузнаваемый — возвращаем "Unknown client". Если узнан только
// клиент или только OS — комбинируем как есть.
func NormalizeUserAgent(raw string) string {
	if raw == "" {
		return "Unknown client"
	}
	low := strings.ToLower(raw)

	client := matchFirst(low, []match{
		{"happ", "Happ"},
		{"v2rayng", "v2RayNG"},
		{"v2rayn", "v2RayN"},
		{"hiddify", "Hiddify"},
		{"streisand", "Streisand"},
		{"shadowrocket", "Shadowrocket"},
		{"foxray", "FoxRay"},
		{"flclash", "FlClash"},
		{"clash", "Clash"},
		{"sing-box", "sing-box"},
		{"singbox", "sing-box"},
		{"nekobox", "NekoBox"},
		{"nekoray", "NekoRay"},
		{"karing", "Karing"},
		{"husi", "Husi"},
		{"v2box", "V2Box"},
		{"throne", "Throne"},
	})

	os := matchFirst(low, []match{
		{"iphone", "iOS"},
		{"ipad", "iPadOS"},
		{"ios", "iOS"},
		{"android", "Android"},
		{"macintosh", "macOS"},
		{"mac os", "macOS"},
		{"darwin", "macOS"},
		{"windows", "Windows"},
		{"linux", "Linux"},
	})

	switch {
	case client != "" && os != "":
		return client + " / " + os
	case client != "":
		return client
	case os != "":
		return os
	default:
		// Подрежем длинный UA до разумного, чтобы не плодить мегадлинные строки
		// в БД (VARCHAR(255), но для UI чем короче — тем лучше).
		return truncate(raw, 60)
	}
}

type match struct {
	needle string
	label  string
}

func matchFirst(haystack string, ms []match) string {
	for _, m := range ms {
		if strings.Contains(haystack, m.needle) {
			return m.label
		}
	}
	return ""
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	// Безопасный обрез по руне, чтобы не порвать utf-8.
	rs := []rune(s)
	if len(rs) <= max {
		return s
	}
	return string(rs[:max]) + "…"
}
