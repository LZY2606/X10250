// Package web 提供复核界面：波形、字节、ACK/NACK、片选与候选置信联动，
// 任一字节可回到边沿；所有业务规则在服务端可测试。
package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"edgebench/internal/decode"
	"edgebench/internal/model"
	"edgebench/internal/store"
)

type Server struct {
	St  *store.Store
	mux *http.ServeMux
}

func New(st *store.Store) *Server {
	s := &Server{St: st, mux: http.NewServeMux()}
	s.mux.HandleFunc("GET /", s.handleReview)
	s.mux.HandleFunc("GET /event/{seq}", s.handleEvent)
	s.mux.HandleFunc("GET /byte/{id}", s.handleByte)
	s.mux.HandleFunc("POST /decision", s.handleDecision)
	s.mux.HandleFunc("POST /params", s.handleParams)
	s.mux.HandleFunc("POST /decode", s.handleDecode)
	s.mux.HandleFunc("GET /export", s.handleExport)
	s.mux.HandleFunc("POST /import", s.handleImport)
	s.mux.HandleFunc("GET /api/events", s.handleAPIEvents)
	return s
}

func (s *Server) Handler() http.Handler { return s.mux }

func (s *Server) stateAt(r *http.Request) (*store.State, int64, error) {
	max, err := s.St.MaxSeq()
	if err != nil {
		return nil, 0, err
	}
	seq := max
	if q := r.URL.Query().Get("seq"); q != "" {
		if v, err := strconv.ParseInt(q, 10, 64); err == nil && v >= 0 && v <= max {
			seq = v
		}
	}
	st, err := s.St.StateAt(seq)
	return st, max, err
}

func (s *Server) handleReview(w http.ResponseWriter, r *http.Request) {
	st, max, err := s.stateAt(r)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	s.renderReview(w, st, max, "")
}

func (s *Server) handleEvent(w http.ResponseWriter, r *http.Request) {
	seq, err := strconv.ParseInt(r.PathValue("seq"), 10, 64)
	if err != nil {
		http.Error(w, "bad seq", 400)
		return
	}
	max, _ := s.St.MaxSeq()
	if seq > max {
		seq = max
	}
	st, err := s.St.StateAt(seq)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	s.renderReview(w, st, max, fmt.Sprintf("正在查看历史事件 evt-%06d：显示当时的依赖版本与可见结论，未被最新计算覆盖。", seq))
}

func (s *Server) handleByte(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	st, max, err := s.stateAt(r)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if st.Result == nil {
		http.Error(w, "no result", 404)
		return
	}
	txn, cand, b := store.FindByte(st.Result, id)
	if b == nil {
		http.Error(w, "byte not found: "+id, 404)
		return
	}
	s.renderByte(w, st, max, txn, cand, b)
}

func (s *Server) handleDecision(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	target := r.FormValue("target")
	action := r.FormValue("action")
	switch action {
	case "accept", "reject", "adjust":
	default:
		http.Error(w, "unknown action", 400)
		return
	}
	max, _ := s.St.MaxSeq()
	d := model.Decision{
		Target: target, Action: action,
		Note: r.FormValue("note"), Reviewer: r.FormValue("reviewer"),
	}
	if _, _, err := s.St.AddEvent("decision", []int64{max}, d); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	http.Redirect(w, r, "/#txn-"+anchorOf(target), http.StatusSeeOther)
}

func anchorOf(target string) string {
	return strings.TrimPrefix(target, "cand-")
}

func (s *Server) handleParams(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	max, _ := s.St.MaxSeq()
	st, err := s.St.StateAt(max)
	if err != nil || st.Params == nil || st.Capture == nil {
		http.Error(w, "no current params/capture", 409)
		return
	}
	next := *st.Params
	next.Version = st.Params.Version + 1
	next.ID = fmt.Sprintf("params-v%d", next.Version)
	var changes []string
	for i := range next.Buses {
		b := &next.Buses[i]
		switch b.Protocol {
		case "spi":
			if v := r.FormValue("spi_mode"); v != "" && v != b.SPIMode {
				changes = append(changes, fmt.Sprintf("%s.spi_mode: %s→%s", b.Name, b.SPIMode, v))
				b.SPIMode = v
			}
		case "sync":
			if v := r.FormValue("sync_polarity"); v != "" && v != b.ClockPolarity {
				changes = append(changes, fmt.Sprintf("%s.clock_polarity: %s→%s", b.Name, b.ClockPolarity, v))
				b.ClockPolarity = v
			}
		}
		if v := r.FormValue("gap_ns"); v != "" {
			if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 && n != b.GapThresholdNs {
				changes = append(changes, fmt.Sprintf("%s.gap_threshold_ns: %d→%d", b.Name, b.GapThresholdNs, n))
				b.GapThresholdNs = n
			}
		}
	}
	if len(changes) == 0 {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	seq, _, err := s.St.AddEvent("param_revision", []int64{max}, map[string]any{
		"from": st.Params.Version, "to": next.Version, "changes": changes,
	})
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if _, err := s.St.AddMaterial("params", &next, seq); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	s.runDecode(st.Capture, &next, seq, w, r)
}

func (s *Server) handleDecode(w http.ResponseWriter, r *http.Request) {
	max, _ := s.St.MaxSeq()
	st, err := s.St.StateAt(max)
	if err != nil || st.Params == nil || st.Capture == nil {
		http.Error(w, "no current params/capture", 409)
		return
	}
	s.runDecode(st.Capture, st.Params, max, w, r)
}

func (s *Server) runDecode(cap *model.Capture, ps *model.ParamSet, parentSeq int64, w http.ResponseWriter, r *http.Request) {
	res := decode.Run(cap, ps)
	seq, _, err := s.St.AddEvent("decode_run", []int64{parentSeq}, map[string]any{
		"result": res.ID, "status": res.Status, "input_digest": res.InputDigest,
	})
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if err := s.St.AddResult(seq, res); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="edgebench-bundle.json"`)
	if err := s.St.Export(w); err != nil {
		http.Error(w, err.Error(), 500)
	}
}

func (s *Server) handleImport(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	f, _, err := r.FormFile("bundle")
	if err != nil {
		http.Error(w, "missing bundle file", 400)
		return
	}
	defer f.Close()
	if err := s.St.Import(f, true); err != nil {
		http.Error(w, "导入失败（数据库已回滚，未停留半写状态）: "+err.Error(), 422)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// handleAPIEvents 以机器可读形式暴露事件日志（判断依据即事件）。
func (s *Server) handleAPIEvents(w http.ResponseWriter, r *http.Request) {
	evs, err := s.St.Events()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(evs)
}
