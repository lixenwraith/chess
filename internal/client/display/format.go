package display

import (
	"encoding/json"
	"fmt"
)

// PrettyPrintJSON prints formatted JSON
func PrettyPrintJSON(v any) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		Print(Red, "Error formatting JSON: %s\n", err.Error())
		return
	}
	fmt.Println(string(data))
}