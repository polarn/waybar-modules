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

func New() Waybar {
	return Waybar{}
}

func (w Waybar) Print() error {
	b, err := json.Marshal(w)
	if err != nil {
		return err
	}
	fmt.Println(string(b))
	return nil
}
