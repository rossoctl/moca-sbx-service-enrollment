// Package audit emits NDJSON events to stdout: one JSON object per line, each
// carrying the event kind, a timestamp, and the event's fields. The CronJob's
// logs are the audit trail; `kubectl logs` reads it back line-oriented.
package audit

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

type Logger struct{}

// Event writes one audit record. Fields are merged over the event kind and
// timestamp; a marshal failure is reported on stderr rather than dropped
// silently.
func (Logger) Event(kind string, fields map[string]any) {
	rec := make(map[string]any, len(fields)+2)
	for k, v := range fields {
		rec[k] = v
	}
	rec["event"] = kind
	rec["ts"] = time.Now().UTC().Format(time.RFC3339Nano)
	b, err := json.Marshal(rec)
	if err != nil {
		fmt.Fprintf(os.Stderr, "audit marshal error: %v\n", err)
		return
	}
	fmt.Println(string(b))
}
