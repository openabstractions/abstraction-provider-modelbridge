package main

import (
	"encoding/json"

	wire "github.com/openabstractions/abstraction-storage/go/abstraction/storage/lend"
)

// marshal prints the ledger as the diagnostic's JSON, one object per entry,
// with LF terminators so a byte comparison reads the same on every platform.
func marshal(lends []wire.Lend) ([]byte, error) {
	type row struct {
		ID      string `json:"id"`
		Engine  string `json:"engine"`
		Store   string `json:"store"`
		Object  string `json:"object"`
		Name    string `json:"name"`
		Link    string `json:"link"`
		Kind    string `json:"kind"`
		Size    int64  `json:"size"`
		Created string `json:"created"`
	}
	rows := make([]row, 0, len(lends))
	for _, l := range lends {
		rows = append(rows, row{ID: l.ID, Engine: l.Engine, Store: l.Store, Object: l.Object, Name: l.Name,
			Link: l.Link, Kind: l.Kind.String(), Size: l.Size, Created: l.Created})
	}
	raw, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}
