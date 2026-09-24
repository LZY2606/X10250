package store

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"edgebench/internal/eventlog"
)

// Manifest describes an exported bundle and pins its schema version.
type Manifest struct {
	Format     string    `json:"format"`
	Version    int       `json:"version"`
	Project    string    `json:"project"`
	ExportedAt time.Time `json:"exportedAt"`
	Events     int       `json:"events"`
	Materials  []string  `json:"materials"`
	SHA256     string    `json:"sha256,omitempty"`
}

// Export writes a deterministic zip bundle: manifest + events.jsonl + blobs.
func (s *Store) Export(ctx context.Context, project string) ([]byte, error) {
	evs, err := s.AllEvents(ctx, project)
	if err != nil {
		return nil, err
	}
	if len(evs) == 0 {
		return nil, fmt.Errorf("%w: project %q has no events", ErrNoSolution, project)
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	matSet := map[string]bool{}
	var mats []string
	write := func(name string, data []byte) error {
		w, err := zw.Create(name)
		if err != nil {
			return err
		}
		_, err = w.Write(data)
		return err
	}
	var eventsBuf bytes.Buffer
	for _, e := range evs {
		row := struct {
			eventlog.Event
			PayloadJSON json.RawMessage `json:"payload"`
		}{Event: e}
		pj, _ := json.Marshal(e.Payload)
		row.PayloadJSON = pj
		b, _ := json.Marshal(row)
		eventsBuf.Write(b)
		eventsBuf.WriteByte('\n')
		// gather referenced materials
		if e.Type == eventlog.EvMaterial {
			if id, ok := e.Payload["materialId"].(string); ok && !matSet[id] {
				matSet[id] = true
				mats = append(mats, id)
			}
		}
	}
	sort.Strings(mats)
	for _, id := range mats {
		fn, media, data, _, err := s.Material(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("%w: missing material %s: %v", ErrFormatCorrupt, id, err)
		}
		_ = media
		if err := write("materials/"+id+"__"+safeName(fn), data); err != nil {
			return nil, err
		}
	}
	if err := write("events.jsonl", eventsBuf.Bytes()); err != nil {
		return nil, err
	}
	m := Manifest{Format: "edgebench-bundle", Version: 1, Project: project,
		ExportedAt: time.Now().UTC(), Events: len(evs), Materials: mats}
	h := sha256.Sum256(eventsBuf.Bytes)
	m.SHA256 = hex.EncodeToString(h[:])
	mb, _ := json.MarshalIndent(m, "", "  ")
	if err := write("manifest.json", mb); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func safeName(n string) string {
	r := strings.NewReplacer("/", "_", "\\", "_", "..", "_", " ", "_")
	return r.Replace(n)
}

// ImportBundle reads a bundle into the given (fresh or different) database.
// It returns the counts restored. Corrupt bytes => ErrFormatCorrupt; a bundle
// referencing an unresolvable parent => ErrNoSolution; oversized bundle =>
// ErrBudgetExhausted.
func (s *Store) ImportBundle(ctx context.Context, data []byte, maxBytes, maxEvents int) (project string, events, materials int, err error) {
	if maxBytes > 0 && len(data) > maxBytes {
		return "", 0, 0, fmt.Errorf("%w: bundle %d bytes > %d", ErrBudgetExhausted, len(data), maxBytes)
	}
	zr, zerr := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if zerr != nil {
		return "", 0, 0, fmt.Errorf("%w: %v", ErrFormatCorrupt, zerr)
	}
	files := map[string]*zip.File{}
	for _, f := range zr.File {
		files[f.Name] = f
	}
	mf, ok := files["manifest.json"]
	if !ok {
		return "", 0, 0, fmt.Errorf("%w: manifest.json missing", ErrFormatCorrupt)
	}
	manifest, err := readJSON[Manifest](mf)
	if err != nil {
		return "", 0, 0, fmt.Errorf("%w: manifest: %v", ErrFormatCorrupt, err)
	}
	if manifest.Format != "edgebench-bundle" || manifest.Version != 1 {
		return "", 0, 0, fmt.Errorf("%w: unsupported bundle %s v%d", ErrFormatCorrupt, manifest.Format, manifest.Version)
	}
	ef, ok := files["events.jsonl"]
	if !ok {
		return "", 0, 0, fmt.Errorf("%w: events.jsonl missing", ErrFormatCorrupt)
	}
	rc, err := ef.Open()
	if err != nil {
		return "", 0, 0, fmt.Errorf("%w: %v", ErrFormatCorrupt, err)
	}
	defer rc.Close()
	eb, err := io.ReadAll(io.LimitReader(rc, int64(maxBytes)+1))
	if err != nil {
		return "", 0, 0, fmt.Errorf("%w: %v", ErrFormatCorrupt, err)
	}
	h := sha256.Sum256(eb)
	if manifest.SHA256 != hex.EncodeToString(h[:]) {
		return "", 0, 0, fmt.Errorf("%w: events.jsonl checksum mismatch (truncated or half-written bundle)", ErrFormatCorrupt)
	}
	// Re-insert in original append order; identities and hashes are verified.
	prev := ""
	count := 0
	for _, line := range bytes.Split(eb, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		if maxEvents > 0 && count >= maxEvents {
			return "", count, materials, fmt.Errorf("%w: >%d events", ErrBudgetExhausted, maxEvents)
		}
		var row struct {
			eventlog.Event
			PayloadJSON json.RawMessage `json:"payload"`
		}
		if err := json.Unmarshal(line, &row); err != nil {
			return "", count, materials, fmt.Errorf("%w: bad event json: %v", ErrFormatCorrupt, err)
		}
		if row.PrevHash != prev {
			return "", count, materials, fmt.Errorf("%w: event %s breaks chain", ErrFormatCorrupt, row.ID)
		}
		var pl map[string]any
		if err := json.Unmarshal(row.PayloadJSON, &pl); err != nil {
			return "", count, materials, fmt.Errorf("%w: event %s payload: %v", ErrFormatCorrupt, row.ID, err)
		}
		want := eventlog.HashEvent(&eventlog.Event{
			Seq: row.Seq, ID: row.ID, Project: row.Project, Branch: row.Branch, Parent: row.Parent,
			Type: row.Type, At: row.At, Payload: pl, PrevHash: prev,
		})
		if want != row.Hash || row.ID != eventlog.StableID(row.Project, row.Seq, row.Type, pl) {
			return "", count, materials, fmt.Errorf("%w: event %s identity/hash mismatch", ErrFormatCorrupt, row.ID)
		}
		count++
		prev = row.Hash
	}
	if count != manifest.Events {
		return "", count, materials, fmt.Errorf("%w: manifest says %d events, found %d", ErrFormatCorrupt, manifest.Events, count)
	}
	// All good: insert blobs first then events (single tx for atomicity).
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", 0, 0, err
	}
	defer tx.Rollback()
	for name, f := range files {
		if !strings.HasPrefix(name, "materials/") {
			continue
		}
		blob, err := readAll(f)
		if err != nil {
			return "", count, materials, fmt.Errorf("%w: material %s: %v", ErrFormatCorrupt, name, err)
		}
		id := strings.TrimPrefix(name, "materials/")
		id = strings.SplitN(id, "__", 2)[0]
		hh := sha256.Sum256(blob)
		fn := strings.SplitN(strings.TrimPrefix(name, "materials/"), "__", 2)
		fileName := id
		if len(fn) == 2 {
			fileName = fn[1]
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO materials(id,filename,media_type,data,sha256,imported_at)
VALUES(?,?,?,?,?,?)`, id, fileName, "application/octet-stream", blob, hex.EncodeToString(hh[:]),
			manifest.ExportedAt.Format(time.RFC3339Nano)); err != nil {
			return "", count, materials, err
		}
		materials++
	}
	for _, line := range bytes.Split(eb, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var row struct {
			eventlog.Event
			PayloadJSON json.RawMessage `json:"payload"`
		}
		_ = json.Unmarshal(line, &row)
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO events
(id,project,branch,parent,type,at,actor,payload,input_hash,prev_hash,hash)
VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
			row.ID, row.Project, row.Branch, row.Parent, string(row.Type),
			row.At.Format(time.RFC3339Nano), row.Actor, string(row.PayloadJSON),
			row.InputHash, row.PrevHash, row.Hash); err != nil {
			return "", count, materials, err
		}
	}
	if err := tx.Commit(); err != nil {
		return "", count, materials, err
	}
	// semantic check: every run references present materials/params.
	evs, _ := s.AllEvents(ctx, manifest.Project)
	haveMat := map[string]bool{}
	for _, id := range manifest.Materials {
		haveMat[id] = true
	}
	haveParent := map[string]bool{}
	for _, e := range evs {
		if e.Type == eventlog.EvParams {
			if v, ok := e.Payload["version"].(float64); ok {
				haveParent[fmt.Sprintf("%s:v%v", e.Project, v)] = true
			}
		}
		if e.Type == eventlog.EvDecode {
			var dp eventlog.DecodePayload
			pj, _ := json.Marshal(e.Payload)
			if json.Unmarshal(pj, &dp) == nil {
				if dp.MaterialID != "" && !haveMat[dp.MaterialID] {
					return "", count, materials, fmt.Errorf("%w: run %s references missing material %s", ErrNoSolution, dp.RunID, dp.MaterialID)
				}
			}
		}
	}
	return manifest.Project, count, materials, nil
}

func readJSON[T any](f *zip.File) (T, error) {
	var out T
	rc, err := f.Open()
	if err != nil {
		return out, err
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		return out, err
	}
	return out, json.Unmarshal(b, &out)
}

func readAll(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}
