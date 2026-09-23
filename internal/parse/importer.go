package parse

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"edgebench/internal/model"
)

// Document is the accepted shape of an import.
type Document struct {
	Schema   int
	Project  ProjectBlock
	Captures []model.Capture
	Evidence []model.ProbeEvidence
	Params   []model.Params
}

// ProjectBlock is the stable project identity inside a case pack.
type ProjectBlock struct {
	ID    string
	Name  string
	Title string
}

type importer struct {
	doc      []byte
	budgets  Budgets
	diags    []model.Diagnostic
	errors   int
	errClass string
}

// fail records a hard error; the import is rejected when any exists.
func (im *importer) fail(n *node, code, class, msg string) {
	im.diags = append(im.diags, im.diagAt(n, code, model.SevError, msg, class, im.rawAt(n)))
	im.errors++
	im.errClass = class
}

func (im *importer) failf(n *node, code, class, format string, args ...any) {
	im.fail(n, code, class, fmt.Sprintf(format, args...))
}

func (im *importer) warn(n *node, code, msg string, args ...any) {
	im.diags = append(im.diags, im.diagAt(n, code, model.SevWarning, fmt.Sprintf(msg, args...), "", im.rawAt(n)))
}

// Import parses and validates a document. A corrupt document, budget
// exhaustion and semantic failure return distinct sentinels; warnings are
// returned with the document.
func Import(doc []byte, budgets Budgets) (*Document, []model.Diagnostic, error) {
	budgets = budgets.withDefaults()
	im := &importer{doc: doc, budgets: budgets}
	if len(doc) > budgets.MaxBytes {
		return nil, nil, fmt.Errorf("%w: document %d bytes exceeds %d", ErrBudget, len(doc), budgets.MaxBytes)
	}
	root, err := parseTree(doc)
	if err != nil {
		return nil, []model.Diagnostic{{
			Code:     "DOCUMENT_NOT_JSON",
			Severity: model.SevError,
			Message:  err.Error(),
			Class:    model.ClassCorrupt,
		}}, ErrCorrupt
	}
	if root.obj == nil {
		im.fail(root, "ROOT_NOT_OBJECT", model.ClassSemantic, "document root must be an object")
		return nil, im.diags, ErrSemantic
	}

	schema := im.requireInt(root.field("schema_version"), "schema_version")
	if schema != model.SchemaVersion {
		im.failf(root.field("schema_version"), "SCHEMA_UNSUPPORTED", model.ClassSemantic,
			"schema_version must be %d, got %d", model.SchemaVersion, schema)
	}

	proj := im.parseProject(root.field("project"))
	params := im.parseParams(root)
	captures, evidence := im.parseCaptures(root)

	if len(captures) > budgets.MaxCaptures {
		return nil, im.diags, fmt.Errorf("%w: captures %d exceed %d", ErrBudget, len(captures), budgets.MaxCaptures)
	}
	if im.errors > 0 {
		class := im.errClass
		if class == "" {
			class = model.ClassSemantic
		}
		return nil, im.diags, classError(class)
	}
	out := &Document{
		Schema:   schema,
		Project:  proj,
		Captures: captures,
		Evidence: evidence,
		Params:   params,
	}
	return out, im.diags, nil
}

func classError(class string) error {
	switch class {
	case model.ClassBudget:
		return ErrBudget
	case model.ClassCorrupt:
		return ErrCorrupt
	default:
		return ErrSemantic
	}
}

func (im *importer) requireInt(n *node, what string) int {
	if n == nil {
		im.fail(n, "FIELD_MISSING", model.ClassSemantic, what+" is required")
		return 0
	}
	num, ok := n.leaf.(json.Number)
	if !ok {
		im.fail(n, "FIELD_TYPE", model.ClassSemantic, what+" must be an integer")
		return 0
	}
	v, err := strconv.Atoi(num.String())
	if err != nil {
		im.fail(n, "FIELD_TYPE", model.ClassSemantic, what+" must be an integer")
		return 0
	}
	return v
}

func (im *importer) parseProject(n *node) ProjectBlock {
	if n == nil {
		im.fail(nil, "PROJECT_MISSING", model.ClassSemantic, "project is required")
		return ProjectBlock{}
	}
	id := im.requireString(n.field("id"), "project.id")
	name := im.requireString(n.field("name"), "project.name")
	title := ""
	if t := n.field("title"); t != nil {
		if s, ok := t.leaf.(string); ok {
			title = s
		}
	}
	return ProjectBlock{ID: id, Name: name, Title: title}
}

func (im *importer) requireString(n *node, what string) string {
	if n == nil {
		im.fail(nil, "FIELD_MISSING", model.ClassSemantic, what+" is required")
		return ""
	}
	s, ok := n.leaf.(string)
	if !ok {
		im.fail(n, "FIELD_TYPE", model.ClassSemantic, what+" must be a string")
		return ""
	}
	if strings.TrimSpace(s) == "" {
		im.fail(n, "FIELD_EMPTY", model.ClassSemantic, what+" must not be empty")
	}
	return s
}

func (im *importer) parseParams(root *node) []model.Params {
	out := []model.Params{}
	pp := root.field("params")
	if pp == nil {
		im.fail(nil, "PARAMS_MISSING", model.ClassSemantic, "params (revision 1 baseline) is required")
		return out
	}
	if pp.arr == nil {
		im.fail(pp, "PARAMS_TYPE", model.ClassSemantic, "params must be an array")
		return out
	}
	revisions := map[int]bool{}
	for i, pn := range pp.arr {
		p := model.Params{SPICPOL: -1}
		rev := im.requireInt(pn.field("revision"), fmt.Sprintf("params[%d].revision", i))
		p.Revision = rev
		if revisions[rev] {
			im.failf(pn.field("revision"), "PARAMS_DUPLICATE_REVISION", model.ClassSemantic,
				"duplicate params revision %d", rev)
		}
		revisions[rev] = true
		p.I2CThresholdStretchSeconds = im.secondsField(pn, "i2c_stretch")
		p.I2CThresholdGapSeconds = im.secondsField(pn, "i2c_gap")
		p.I2CBoundaryWindowSeconds = im.secondsField(pn, "i2c_boundary_window")
		p.SPIThresholdGapSeconds = im.secondsField(pn, "spi_gap")
		p.SPIBoundaryWindowSeconds = im.secondsField(pn, "spi_boundary_window")
		p.CustomThresholdGapSeconds = im.secondsField(pn, "custom_gap")
		p.CustomBoundaryWindowSeconds = im.secondsField(pn, "custom_boundary_window")
		if cn := pn.field("custom_sync"); cn != nil {
			if s, ok := cn.leaf.(string); ok {
				p.CustomSync = s
			}
		}
		p.SPICPOL, p.SPICPHA = im.spiMode(pn)
		out = append(out, p)
	}
	if !revisions[model.InitialParamsRevision] {
		im.failf(pp, "PARAMS_BASELINE_MISSING", model.ClassSemantic,
			"params revision %d baseline is required", model.InitialParamsRevision)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Revision < out[j].Revision })
	return out
}

func (im *importer) spiMode(pn *node) (cpol, cpha int) {
	cpol, cpha = -1, 0
	if mn := pn.field("spi_mode"); mn != nil {
		s, ok := mn.leaf.(string)
		if !ok {
			im.fail(mn, "SPI_MODE_TYPE", model.ClassSemantic, "spi_mode must be a string like mode0..mode3 or unknown")
			return
		}
		switch strings.ToLower(s) {
		case "unknown", "":
			cpol = -1
		case "mode0":
			cpol, cpha = 0, 0
		case "mode1":
			cpol, cpha = 0, 1
		case "mode2":
			cpol, cpha = 1, 0
		case "mode3":
			cpol, cpha = 1, 1
		default:
			im.failf(mn, "SPI_MODE_VALUE", model.ClassSemantic, "unknown spi_mode %q (mode0..mode3|unknown)", s)
		}
	}
	return cpol, cpha
}

func (im *importer) secondsField(pn *node, name string) float64 {
	f := pn.field(name)
	if f == nil {
		im.failf(pn, "FIELD_MISSING", model.ClassSemantic, "%s is required (explicit quantity, no implicit default)", name)
		return 0
	}
	q := im.parseQty(f)
	secs, ok := timeToSeconds(q)
	if !ok {
		im.failf(f, "UNIT_UNCONVERTIBLE", model.ClassSemantic,
			"%s=%q cannot be converted to seconds", name, q.Raw)
		return 0
	}
	if secs <= 0 {
		im.failf(f, "FIELD_RANGE", model.ClassSemantic, "%s must be > 0", name)
	}
	return secs
}
