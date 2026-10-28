package llm

import (
	"strings"
)

func approvalDecision(text string) string {
	s := strings.ToLower(strings.TrimSpace(text))
	s = strings.Trim(s, " .,!?:;\t\n\r")
	switch s {
	case "разрешаю", "подтверждаю", "разреши", "да, разрешаю":
		return "allow"
	case "отмена", "отменяю", "не разрешаю", "отклоняю", "нет, отмена":
		return "reject"
	default:
		return ""
	}
}
