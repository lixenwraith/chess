// FILE: lixenwraith/chess/internal/client/display/format.go
package display

import (
	"encoding/json"
	"fmt"
)

// PrettyPrintJSON prints formatted JSON
func PrettyPrintJSON(v interface{}) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		fmt.Printf("%sError formatting JSON: %s%s\n", Red, err.Error(), Reset)
		return
	}
	fmt.Println(string(data))
}