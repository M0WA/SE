package application

import (
	"context"
	"encoding/json"
	"log"

	"searchengine/internal/ports"
)

// mapKeys returns m's keys as a slice, in no particular order -- shared by
// call sites that just need a map's key set as a []string.
func mapKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// loadJSONStatus reads a JSON-encoded status blob from settings under key
// -- shared by every background job's Load*Status function (pagerank,
// embedding recompute, content dedup) instead of each hand-rolling the
// same "missing/bad -> zero value" shape. what names the status in the
// log line a decode failure produces.
func loadJSONStatus[T any](ctx context.Context, settings ports.SettingsStore, key, what string) T {
	var zero T
	if settings == nil {
		return zero
	}
	value, found, err := settings.GetSetting(ctx, key)
	if err != nil || !found {
		return zero
	}
	var status T
	if err := json.Unmarshal([]byte(value), &status); err != nil {
		log.Printf("decoding %s: %v", what, err)
		return zero
	}
	return status
}

// saveJSONStatus is loadJSONStatus's write side -- see its doc comment.
func saveJSONStatus[T any](ctx context.Context, settings ports.SettingsStore, key, what string, status T) {
	if settings == nil {
		return
	}
	data, err := json.Marshal(status)
	if err != nil {
		log.Printf("encoding %s: %v", what, err)
		return
	}
	if err := settings.SaveSetting(ctx, key, string(data)); err != nil {
		log.Printf("saving %s: %v", what, err)
	}
}
