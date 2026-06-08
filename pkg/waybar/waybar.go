package waybar

import (
	"encoding/json"
	"fmt"
)

type Waybar struct {
	Text    string `json:"text"`
	ToolTip string `json:"tooltip,omitempty"`
	// Class is a single CSS class (string) or several (e.g. []string).
	// waybar applies every entry of an array as a GTK style class, which
	// lets a module combine independent state dimensions (e.g. a severity
	// class plus a gauge-bucket class).
	Class any    `json:"class,omitempty"`
	Alt   string `json:"alt,omitempty"`
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
