package bridge

import "testing"

func TestTrimStreamedPrefix(t *testing.T) {
	tests := []struct {
		name, answer, spoken, want string
	}{
		{"spoken prefix", "Первая фраза. Вторая.", "Первая фраза. ", "Вторая."},
		{"all spoken", "Первая фраза.", "Первая фраза. ", ""},
		{"mismatch keeps full answer", "Итоговый ответ.", "Другая фраза. ", "Итоговый ответ."},
		{"no streamed text", "Итоговый ответ.", "", "Итоговый ответ."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := trimStreamedPrefix(tt.answer, tt.spoken); got != tt.want {
				t.Fatalf("trimStreamedPrefix() = %q, want %q", got, tt.want)
			}
		})
	}
}
