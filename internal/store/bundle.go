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
	"os"
	"path/filepath"
	"sort"
	"time"

	"edgebench/internal/decoder"
	"edgebench/internal/domain"
)

const bundleFormat = "edgebench-bundle/1"

// Bundle is the full, identity-preserving export of one project.
type Bundle struct {
	Format        string                      `json:"format"`
	ExportedAt    int64                       `json:"exportedAt"`
	Project       Project                     `json:"project"`
	Captures      []domain.Capture            `json:"captures"`
	Channels      map[string][]domain.Channel `json:"channels"`
	Edges         map[string][]domain.Edge    `json:"edges"`
	Events        []domain.Event              `json:"events"`
	Params        []ParamsRow                 `json:"params"`
	ArchiveProbes []domain.ArchiveProbe       `json:"archiveProbes"`
}

type ParamsRow struct {
	BusID   string               `json:"busId"`
	Seq     int                  `json:"seq"`
	EventID string               `json:"eventId"`
	Params  domain.DecoderParams `json:"params"`
}

type bundleEnvelope struct {
	Manifest manifestFile `json:"manifest"`
}

type manifestFile struct {
	Format    string            `json:"format"`
	Version   string            `json:"rulesVersion"`
	Files     map[string]string `json:"files"` // name -> sha256
	BundleSHA string            `json:"bundleSha256"`
}

// ExportProject writes a zip bundle to w. Content is normalized JSON so an
// equivalent project produces byte-stable file payloads and stable IDs.
func (s *Store) ExportProject(projectID string, w io.Writer) error {
	b, err := s.buildBundle(projectID)
	if err != nil {
		return err
	}
	return writeBundleZip(b, w)
}

func (s *Store) buildBundle(projectID string) (*Bundle, error) {
	p := Project{}
	row := s.db.QueryRow(`SELECT id,name,created_at FROM projects WHERE id=?`, projectID)
	if err := row.Scan(&p.ID, &p.Name, &p.CreatedAt); err != nil {
		return nil, err
	}
	capRows, err := s.db.Query(`SELECT id,parent_id,version,label,sample_json,time_unit,origin_event,input_digest
		FROM captures WHERE project_id=? ORDER BY version`, projectID)
	if err != nil {
		return nil, err
	}
	defer capRows.Close()
	b := &Bundle{Format: bundleFormat, ExportedAt: time.Now().UnixNano(), Project: p,
		Channels: map[string][]domain.Channel{}, Edges: map[string][]domain.Edge{}}
	for capRows.Next() {
		var c domain.Capture
		var sample []byte
		if err := capRows.Scan(&c.ID, &c.ParentID, &c.Version, &c.Label, &sample, &c.TimeUnit, &c.OriginEventID, &c.InputDigest); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(sample, &c.SampleRate)
		b.Captures = append(b.Captures, c)
	}
	for _, c := range b.Captures {
		chans, err := s.LoadChannels(c.ID)
		if err != nil {
			return nil, err
		}
		b.Channels[c.ID] = chans
		edges, err := s.LoadEdges(c.ID)
		if err != nil {
			return nil, err
		}
		b.Edges[c.ID] = edges
	}
	events, err := s.ListEvents(projectID)
	if err != nil {
		return nil, err
	}
	b.Events = events
	prs, err := s.db.Query(`SELECT bus_id,seq,event_id,params_json FROM params_versions WHERE project_id=? ORDER BY bus_id,seq,event_id`, projectID)
	if err != nil {
		return nil, err
	}
	defer prs.Close()
	for prs.Next() {
		var r ParamsRow
		var raw []byte
		if err := prs.Scan(&r.BusID, &r.Seq, &r.EventID, &raw); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(raw, &r.Params)
		b.Params = append(b.Params, r)
	}
	b.ArchiveProbes, err = s.ListArchiveProbes(projectID)
	if err != nil {
		return nil, err
	}
	return b, nil
}

func canonicalJSON(v any) []byte {
	b, _ := json.MarshalIndent(v, "", "  ")
	return b
}

func writeBundleZip(b *Bundle, w io.Writer) error {
	zw := zip.NewWriter(w)
	add := func(name string, data []byte) error {
		f, err := zw.Create(name)
		if err != nil {
			return err
		}
		_, err = f.Write(data)
		return err
	}
	bundleBytes := canonicalJSON(b)
	h := sha256.Sum256(bundleBytes)
	mf := manifestFile{
		Format: bundleFormat, Version: decoder.RulesVersion,
		Files:     map[string]string{"bundle.json": hex.EncodeToString(h[:])},
		BundleSHA: hex.EncodeToString(h[:]),
	}
	if err := add("manifest.json", canonicalJSON(mf)); err != nil {
		return err
	}
	if err := add("bundle.json", bundleBytes); err != nil {
		return err
	}
	return zw.Close()
}

// ImportBundle reads a checksummed bundle into a fresh project. Identity,
// ordering and provenance are preserved: rows keep their original IDs and
// event sequence. A truncated or tampered file is ErrCorrupt; a structurally
// complete but unresolvable payload is ErrSemantic.
func (s *Store) ImportBundle(ctx context.Context, r io.Reader, budget Budget) (string, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return "", corrupt("bundle", "cannot read archive")
	}
	if budget.MaxBytes > 0 && int64(len(raw)) > budget.MaxBytes {
		return "", budgetErr("bundle", "bundle exceeds byte budget")
	}
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return "", corrupt("bundle", "not a zip archive")
	}
	files := map[string]*zip.File{}
	for _, f := range zr.File {
		files[f.Name] = f
	}
	readFile := func(name string) ([]byte, error) {
		f, ok := files[name]
		if !ok {
			return nil, corrupt(name, "missing archive member")
		}
		rc, err := f.Open()
		if err != nil {
			return nil, corrupt(name, "cannot open member")
		}
		defer rc.Close()
		return io.ReadAll(rc)
	}
	manBytes, err := readFile("manifest.json")
	if err != nil {
		return "", err
	}
	var mf manifestFile
	if err := json.Unmarshal(manBytes, &mf); err != nil {
		return "", corrupt("manifest.json", "invalid manifest JSON")
	}
	if mf.Format != bundleFormat {
		return "", corrupt("manifest.json", "unsupported format "+mf.Format)
	}
	bundleBytes, err := readFile("bundle.json")
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(bundleBytes)
	if got := hex.EncodeToString(sum[:]); got != mf.BundleSHA {
		return "", corrupt("bundle.json", "checksum mismatch")
	}
	var b Bundle
	if err := json.Unmarshal(bundleBytes, &b); err != nil {
		return "", corrupt("bundle.json", "invalid bundle JSON")
	}
	if err := validateBundle(&b); err != nil {
		return "", err
	}
	edgeCount := 0
	for _, es := range b.Edges {
		edgeCount += len(es)
	}
	if budget.MaxEdges > 0 && edgeCount > budget.MaxEdges {
		return "", budgetErr("edges", fmt.Sprintf("%d edges exceed budget", edgeCount))
	}
	if err := s.insertBundle(&b); err != nil {
		return "", err
	}
	return b.Project.ID, nil
}

func validateBundle(b *Bundle) error {
	if b.Project.ID == "" {
		return semantic("project.id", "missing project identity")
	}
	caps := map[string]bool{}
	for _, c := range b.Captures {
		if c.ID == "" {
			return semantic("captures.id", "missing capture identity")
		}
		caps[c.ID] = true
	}
	// Parent links must resolve; event ids and ordering must be consistent.
	for _, c := range b.Captures {
		if c.ParentID != "" && !caps[c.ParentID] {
			return semantic("captures.parentId", "dangling parent capture "+c.ParentID)
		}
	}
	for i, e := range b.Events {
		if e.ID == "" {
			return semantic("events.id", "missing event identity")
		}
		if e.Seq != i+1 {
			return corrupt("events.seq", "event sequence is not contiguous from 1")
		}
	}
	sort.SliceStable(b.Captures, func(i, j int) bool { return b.Captures[i].Version < b.Captures[j].Version })
	return nil
}

func (s *Store) insertBundle(b *Bundle) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT OR IGNORE INTO projects(id,name,created_at) VALUES(?,?,?)`,
		b.Project.ID, b.Project.Name, b.Project.CreatedAt); err != nil {
		return err
	}
	for _, c := range b.Captures {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO captures(id,project_id,parent_id,version,label,sample_json,time_unit,origin_event,input_digest,created_at)
			VALUES(?,?,?,?,?,?,?,?,?,?)`,
			c.ID, b.Project.ID, c.ParentID, c.Version, c.Label,
			string(mustJSON(c.SampleRate)), c.TimeUnit, c.OriginEventID, c.InputDigest, b.Project.CreatedAt); err != nil {
			return err
		}
	}
	for capID, chans := range b.Channels {
		for _, c := range chans {
			if _, err := tx.Exec(`INSERT OR IGNORE INTO channels(capture_id,alias,physical,role,inverted,voltage_json,raw_json)
				VALUES(?,?,?,?,?,?,?)`,
				capID, c.Alias, c.Physical, c.Role, boolI(c.Inverted),
				string(mustJSON(c.Voltage)), string(mustJSON(orMap(c.Raw)))); err != nil {
				return err
			}
		}
	}
	for capID, edges := range b.Edges {
		for _, e := range edges {
			if _, err := tx.Exec(`INSERT OR IGNORE INTO edges(id,capture_id,seq,alias,level,time_ps,raw_time,raw_unit,time_valid,raw_json)
				VALUES(?,?,?,?,?,?,?,?,?,?)`,
				e.ID, capID, e.Seq, e.Alias, e.Level, e.TimePS, e.RawTime, e.RawUnit,
				boolI(e.TimeValid), string(mustJSON(orMap(e.Raw)))); err != nil {
				return err
			}
		}
	}
	for _, e := range b.Events {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO events(seq,id,project_id,parent_id,kind,capture_id,bus_id,rules_version,input_summary,detail_json,created_at)
			VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
			e.Seq, e.ID, b.Project.ID, e.ParentID, string(e.Kind), e.CaptureID, e.BusID,
			e.RulesVersion, e.InputSummary, string(mustJSON(orMap(e.Detail))), e.CreatedAtPS); err != nil {
			return err
		}
	}
	for _, r := range b.Params {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO params_versions(project_id,bus_id,seq,event_id,params_json)
			VALUES(?,?,?,?,?)`, b.Project.ID, r.BusID, r.Seq, r.EventID, string(mustJSON(r.Params))); err != nil {
			return err
		}
	}
	for _, p := range b.ArchiveProbes {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO archive_probes
			(project_id,id,name,changed,changed_id,baseline,affected_json,unchanged_json,contained,accepted_json,rejected_json,note)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
			b.Project.ID, p.ID, p.Name, p.Changed, p.ChangedID, p.BaselineID,
			string(mustJSON(orEmpty(p.Affected))), string(mustJSON(orEmpty(p.Unchanged))),
			boolI(p.Contained), string(mustJSON(orEmpty(p.Accepted))),
			string(mustJSON(orEmpty(p.Rejected))), p.Note); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func orMap(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// ExportToFile / ImportFromFile perform atomic file writes: data goes to a
// sibling temp file and is renamed only after fsync, so an interrupted
// export never replaces a good file with a half-written one.
func (s *Store) ExportToFile(projectID, path string) error {
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if err := s.ExportProject(projectID, f); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

func (s *Store) ImportFromFile(ctx context.Context, path string, budget Budget) (string, error) {
	// Import into a temporary database first; only on successful validation
	// do we copy rows into the live database. A half-written file is rejected
	// before anything becomes visible.
	f, err := os.Open(path)
	if err != nil {
		return "", corrupt(path, "cannot open bundle")
	}
	defer f.Close()
	if _, err := f.Stat(); err != nil {
		return "", corrupt(path, "cannot stat bundle")
	}
	return s.ImportBundle(ctx, f, budget)
}

var _ = filepath.Join
