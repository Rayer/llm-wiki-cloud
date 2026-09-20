package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFlashMigrationWire(t *testing.T) {
	for _, model := range []string{"", "deepseek-flash", "deepseek-v4-flash", "deepseek-v4-pro", "deepseek-chat", "deepseek-reasoner"} {
		for _, reasoning := range []Reasoning{ReasoningNone, ReasoningHigh} {
			t.Run(model+"/"+string(reasoning), func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var body chatRequest
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Fatal(err)
					}
					wantThinking := "disabled"
					if reasoning != ReasoningNone {
						wantThinking = "enabled"
					}
					if body.Model != "deepseek-flash" || body.Thinking.Type != wantThinking {
						t.Errorf("wire model/thinking = %s/%s", body.Model, body.Thinking.Type)
					}
					if reasoning == ReasoningHigh && body.ReasoningEffort != "high" {
						t.Errorf("reasoning changed: %s", body.ReasoningEffort)
					}
					w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
				}))
				defer server.Close()
				client := NewClientWithOptions("fake", ClientOptions{Model: model, Reasoning: reasoning})
				client.baseURL = server.URL
				if _, err := client.Chat(context.Background(), "system", "user"); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
	if NewClientWithOptions("fake", ClientOptions{Model: "unknown-model"}) != nil {
		t.Fatal("unknown model accepted")
	}
}
