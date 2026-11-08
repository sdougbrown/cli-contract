// Package envelope builds the error envelope the reference adapters write: the
// command, the failure status, and the error's code and message. The black-box
// checker and the reference fixture runner both use it, so a change to the wire
// shape reaches every producer at once.
package envelope

// Error builds the envelope for one failed command from its error code and the
// error the command raised.
func Error(command, code string, err error) map[string]any {
	return map[string]any{
		"command": command,
		"status":  "error",
		"error":   map[string]any{"code": code, "message": err.Error()},
	}
}
