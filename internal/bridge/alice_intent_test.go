package bridge

import "testing"

func TestAliceIntentRoutesBuiltInCommands(t *testing.T) {
	tests := []struct {
		text string
		want string
	}{
		{"Поставь таймер на 5 минут", "timer"},
		{"Засеки десять минут", "timer"},
		{"Отмени будильник", "alarm"},
		{"Напомни купить молоко вечером", "reminder"},
		{"Какая погода завтра в Москве?", "weather"},
		{"Какой сегодня день недели?", "time_date"},
		{"Сколько будет 17 умножить на 8", "calculation"},
		{"Включи музыку", "media"},
		{"Поставь на паузу", "media"},
		{"Выключи свет на кухне", "smart_home"},
		{"Расскажи, почему меняется погода", ""},
		{"Сколько времени займёт сборка проекта?", ""},
		{"Как реализовать таймер в Go?", ""},
	}
	for _, tt := range tests {
		t.Run(tt.text, func(t *testing.T) {
			if got := aliceIntent(tt.text); got != tt.want {
				t.Fatalf("aliceIntent(%q) = %q, want %q", tt.text, got, tt.want)
			}
		})
	}
}
