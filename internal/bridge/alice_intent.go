package bridge

import (
	"strings"
	"unicode"
)

// aliceIntent recognizes a small set of built-in Station commands locally.
// UniWS exposes ASR text before Alice's cloud NLU runs, but not its intent label.
func aliceIntent(text string) string {
	words := intentWords(text)
	if len(words) == 0 {
		return ""
	}
	joined := strings.Join(words, " ")

	switch {
	case timerCommand(words, joined):
		return "timer"
	case startsWithAny(joined, "будильник", "разбуди") || hasCommandObject(words, []string{"постав", "запусти", "отмени", "выключ", "удали"}, "будильник"):
		return "alarm"
	case startsWithAny(joined, "напомни", "напомните", "создай напоминание"):
		return "reminder"
	case startsWithAny(joined, "погода", "погоду", "какая погода", "что с погодой", "прогноз", "какой прогноз", "узнай погоду", "расскажи погоду", "покажи погоду", "будет ли дождь", "будет ли снег"):
		return "weather"
	case isTimeDateQuery(joined):
		return "time_date"
	case startsWithAny(joined,
		"посчитай", "вычисли", "сколько будет", "корень из",
	):
		return "calculation"
	case aliceMediaCommand(words, joined):
		return "media"
	case aliceSmartHomeCommand(words):
		return "smart_home"
	default:
		return ""
	}
}

func isTimeDateQuery(joined string) bool {
	return startsWithAny(joined,
		"который час", "сколько сейчас времени", "сколько времени сейчас",
		"какое сегодня число", "какая сегодня дата", "какой сегодня день",
		"какой день недели", "какое число", "какая дата",
	) || joined == "сколько времени"
}

func timerCommand(words []string, joined string) bool {
	if startsWithAny(joined, "таймер", "засеки", "отсчитай") {
		return true
	}
	return hasCommandObject(words, []string{"постав", "запусти", "завед", "установ", "отмени", "останов", "сброс", "выключ", "удали"}, "таймер")
}

func hasCommandObject(words, actions []string, object string) bool {
	action, foundObject := false, false
	for _, word := range words {
		if strings.HasPrefix(word, object) {
			foundObject = true
		}
		for _, prefix := range actions {
			if strings.HasPrefix(word, prefix) {
				action = true
			}
		}
	}
	return action && foundObject
}

func intentWords(text string) []string {
	var b strings.Builder
	for _, r := range strings.ToLower(text) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		} else {
			b.WriteByte(' ')
		}
	}
	return strings.Fields(b.String())
}

func hasWordPrefix(words []string, prefixes ...string) bool {
	for _, word := range words {
		for _, prefix := range prefixes {
			if strings.HasPrefix(word, prefix) {
				return true
			}
		}
	}
	return false
}

func startsWithAny(text string, prefixes ...string) bool {
	for _, prefix := range prefixes {
		if text == prefix || strings.HasPrefix(text, prefix+" ") {
			return true
		}
	}
	return false
}

func aliceMediaCommand(words []string, joined string) bool {
	if startsWithAny(joined,
		"громче", "тише", "пауза", "поставь на паузу", "продолжи музыку",
		"следующий трек", "предыдущий трек", "включи музыку", "поставь музыку",
		"включи радио", "поставь радио", "останови музыку", "выключи музыку",
	) {
		return true
	}
	action := hasWordPrefix(words, "включ", "выключ", "постав", "запусти", "останов", "продолж", "переключ")
	media := hasWordPrefix(words, "музык", "песн", "радио", "трек", "альбом", "плейлист", "громк", "звук")
	return action && media
}

func aliceSmartHomeCommand(words []string) bool {
	action := hasWordPrefix(words, "включ", "выключ", "открой", "закрой", "установи")
	device := hasWordPrefix(words,
		"свет", "ламп", "розет", "штор", "жалюз", "кондиционер", "термостат",
		"увлажнител", "очистител", "обогревател", "вентилятор",
	)
	return action && device
}
