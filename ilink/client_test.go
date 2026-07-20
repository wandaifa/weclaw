package ilink

import "testing"

func TestSelectClient(t *testing.T) {
	first := NewClient(&Credentials{ILinkBotID: "first@im.bot"})
	second := NewClient(&Credentials{ILinkBotID: "second@im.bot"})

	t.Run("single account permits omitted bot id", func(t *testing.T) {
		got, err := SelectClient([]*Client{first}, "")
		if err != nil || got != first {
			t.Fatalf("SelectClient() = %v, %v; want first, nil", got, err)
		}
	})

	t.Run("multiple accounts require bot id", func(t *testing.T) {
		if _, err := SelectClient([]*Client{first, second}, ""); err == nil {
			t.Fatal("SelectClient() error = nil, want missing bot_id error")
		}
	})

	t.Run("selects exact bot", func(t *testing.T) {
		got, err := SelectClient([]*Client{first, second}, "second@im.bot")
		if err != nil || got != second {
			t.Fatalf("SelectClient() = %v, %v; want second, nil", got, err)
		}
	})

	t.Run("rejects unknown bot", func(t *testing.T) {
		if _, err := SelectClient([]*Client{first}, "missing@im.bot"); err == nil {
			t.Fatal("SelectClient() error = nil, want unknown bot error")
		}
	})
}
