package tui

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	tea "charm.land/bubbletea/v2"
)

const hitokotoURL = "https://v1.hitokoto.cn/?c=a&c=b&c=d&encode=json"

type hitokotoMsg string

func fetchHitokoto() tea.Msg {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, hitokotoURL, nil)
	if err != nil {
		return hitokotoMsg("")
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return hitokotoMsg("")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return hitokotoMsg("")
	}
	var body struct {
		Text string `json:"hitokoto"`
	}
	if json.NewDecoder(response.Body).Decode(&body) != nil {
		return hitokotoMsg("")
	}
	return hitokotoMsg(body.Text)
}
