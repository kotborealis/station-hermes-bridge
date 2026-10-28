package llm

// Message is the small prompt format accepted by the Hermes Runs API.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
