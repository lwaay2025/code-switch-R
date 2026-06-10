package services

import (
	"encoding/json"
	"os"
	"time"
)

var debugLogPath = "d:/项目/code-switch-R/.cursor/debug-e00ecd.log"

func writeDebugLog(hypothesisID, msg string, data map[string]interface{}) {
	entry := map[string]interface{}{
		"sessionId":    "e00ecd",
		"timestamp":    time.Now().UnixMilli(),
		"hypothesisID": hypothesisID,
		"message":      msg,
		"data":         data,
	}
	line, _ := json.Marshal(entry)
	f, err := os.OpenFile(debugLogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	f.Write(line)
	f.Write([]byte("\n"))
}
