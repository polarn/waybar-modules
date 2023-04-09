package waybar

import (
	"encoding/json"
	"fmt"
)

type Waybar struct {
	Text    string `json:"text"`
	ToolTip string `json:"tooltip,omitempty"`
	Class   string `json:"class,omitempty"`
	Alt     string `json:"alt,omitempty"`
}

func WayBar() Waybar {
	waybar := Waybar{}
	return waybar
}

func (w Waybar) Print() error {
	b, error := json.Marshal(w)
	if error != nil {
		return error
	}
	fmt.Println(string(b))
	return nil
}
