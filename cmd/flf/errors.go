package main

import (
	"encoding/json"
	"io"
	"os"
)

type cliError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeCLIError(w io.Writer, code, message string) int {
	_ = json.NewEncoder(w).Encode(cliError{Code: code, Message: message})
	return exitCodeFor(code)
}

func fail(code, message string) int {
	return writeCLIError(os.Stderr, code, message)
}

func exitCodeFor(code string) int {
	switch code {
	case "DAEMON_DOWN", "DAEMON_ERROR", "DELIVERY_UNKNOWN":
		return 2
	default:
		return 1
	}
}
