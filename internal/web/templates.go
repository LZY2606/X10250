package web

const reviewHTML = `<!doctype html>
<html lang="zh"><head><meta charset="utf-8"><title>边沿译码台 · 复核</title>
<style>
body{background:#0d1117;color:#c9d1d9;font-family:system-ui,sans-serif;margin:0;padding:16px}
h1{font-size:18px}h2{font-size:15px;border-bottom:1px solid #21262d;padding-bottom:4px}
table{border-collapse:collapse;width:100%;font-size:12px}
td,th{border:1px solid #21262d;padding:3px 6px;text-align:left}
a{color:#58a6ff}
.meta{font-family:monospace;font-size:12px;color:#8b949e}
.badge{padding:1px 6px;border-radius:8px;font-size:11px}
.ok{background:#1a7f37}.partial{background:#9e6a03}.unsolvable,.corrupt{background:#b62324}
.budget_exhausted{background:#8957e5}
.pending{background:#30363d}.accepted{background:#1a7f37}.rejected{background:#b62324}
.conflicted{background:#d29922;color:#000}
.ack{background:#1a7f37}.nack{background:#b62324}
.ambig{outline:1px dashed #e3b341}
.confbar{background:#21262d;height:8px;border-radius:4px}
.conffill{background:#3fb950;height:8px;border-radius:4px}
form.inline{display:inline}
input,select,textarea{background:#0d1117;color:#c9d1d9;border:1px solid #30363d;font-size:12px}
button{background:#21262d;color:#c9d1d9;border:1px solid #30363d;border-radius:4px;padding:2px 10px;cursor:pointer}
.banner{background:#332701;border:1px solid #9e6a03;padding:6px 10px;margin:8px 0}
.diag{font-size:11px;color:#f0883e}
.byte{font-family:monospace}
section{margin-bottom:22px}
</style></head><body>
<h1>边沿译码台：逻辑分析波形、I2C/SPI 交易与重叠候选</h1>
{{if .Banner}}<div class="banner">{{.Banner}} <a href="/">回到最新状态</a></div>{{end}}

<section><h2>来源与版本（不可变材料层）</h2>
<div class="meta">
{{with .State.Capture}}采集 {{.ID}} ｜ 时间基准 {{.TimeUnit}} ｜ 采样率 {{.SampleRateHz}} Hz ｜ 通道数 {{len .Channels}}<br>{{end}}
{{with .State.Params}}参数版本 v{{.Version}}（{{.ID}}）｜ 解码器版本 {{.DecoderVersion}}{{end}}<br>
{{with .State.Result}}结果 {{.ID}} ｜ 状态 <span class="badge {{.Status}}">{{.Status}}</span> ｜ 输入摘要 {{.InputDigest}} ｜ 参数版本 v{{.ParamVersion}} ｜ 父级依赖 {{range .Parents}}{{.}} {{end}}{{end}}
</div>
{{range .State.Materials}}<div class="meta">材料 {{.ID}} ｜ {{.Kind}} ｜ 摘要 {{.Digest}} ｜ 来自事件 evt-{{printf "%06d" .Seq}}</div>{{end}}
</section>

<section><h2>波形（边沿 ↔ 字节联动，红色虚线 = 数据缺口/时钟缺边）</h2>
{{.Waveform}}
</section>

<section><h2>交易与候选（字节、ACK/NACK、片选、候选置信联动）</h2>
{{range .Txns}}
<div id="txn-{{.Txn.ID}}" style="margin-bottom:14px">
<div class="meta">交易 {{.Txn.ID}} ｜ 总线 {{.Txn.Bus}} ｜ 协议 {{.Txn.Protocol}} ｜ 设备/片选 {{.Txn.Device}}
｜ {{.Txn.StartNs}}..{{.Txn.EndNs}} ns（{{us .Txn.StartNs}} 起）｜ 状态 <span class="badge {{.Txn.Status}}">{{.Txn.Status}}</span></div>
{{range .Txn.Diagnostics}}<div class="diag">⚠ [{{.Code}}] {{.Message}}（位置：{{.Channel}}#{{.EdgeIdx}} @ {{.TimeNs}} ns）</div>{{end}}
{{range .Cands}}
<table><tr><th style="width:230px">候选 / 置信</th><th>字节（点击回到边沿）</th><th style="width:250px">决定</th></tr>
<tr><td>{{.Cand.ID}}<br>{{.Cand.Decoder}} <span class="meta">{{.Cand.Reason}}</span><br>
<div class="confbar"><div class="conffill" style="width:{{pct .Cand.Confidence}}"></div></div>{{pct .Cand.Confidence}}
｜ <span class="badge {{.Status}}">{{.Status}}</span></td>
<td>{{range .Cand.Bytes}}<a class="byte {{if .Ambig}}ambig{{end}}" href="/byte/{{.ID}}?seq={{$.State.Seq}}" title="{{.Role}} {{.StartNs}}..{{.EndNs}} ns">{{hex .Value}}</a>{{if .Ack}} <span class="badge {{if ack .}}ack{{else}}nack{{end}}">{{ack .}}</span>{{end}} {{end}}</td>
<td>{{range .Decisions}}<div class="meta">{{.Reviewer}}: {{.Action}} — {{.Note}} (evt)</div>{{end}}
<form class="inline" method="post" action="/decision">
<input type="hidden" name="target" value="{{.Cand.ID}}">
<input name="reviewer" placeholder="审阅者" size="6">
<select name="action"><option>accept</option><option>reject</option><option>adjust</option></select>
<input name="note" placeholder="判断依据" size="18"><button>记录决定</button></form></td></tr>
</table>
{{end}}
</div>
{{end}}
</section>

<section><h2>诊断（带位置；输入不完整不伪装成功）</h2>
{{with .State.Result}}{{range .Diagnostics}}<div class="diag">[{{.Code}}] {{.Message}}（{{.Bus}} {{.Channel}}#{{.EdgeIdx}} @ {{.TimeNs}} ns）</div>{{end}}{{end}}
</section>

<section><h2>操作（导入 / 计算 / 保存 / 导出 / 再次打开）</h2>
<form class="inline" method="post" action="/decode"><button>重新解码（当前材料）</button></form>
<form class="inline" method="post" action="/params">
SPI mode <select name="spi_mode"><option value="">不变</option><option>unknown</option><option>0</option><option>1</option><option>2</option><option>3</option></select>
同步极性 <select name="sync_polarity"><option value="">不变</option><option>unknown</option><option>rising</option><option>falling</option></select>
缺口阈值 ns <input name="gap_ns" size="7" placeholder="不变">
<button>参数修订并解码</button></form>
<a href="/export"><button>导出档案</button></a>
<form class="inline" method="post" action="/import" enctype="multipart/form-data">
<input type="file" name="bundle"><button>导入（替换本地库）</button></form>
</section>

<section><h2>事件时间线（点击恢复当时结论）</h2>
{{range .State.Events}}<div class="meta">
<a href="/event/{{.Seq}}">{{.ID}}</a> ｜ {{.Kind}} ｜ 父事件 {{range .Parents}}evt-{{printf "%06d" .}} {{end}} ｜ {{.CreatedAt}}</div>{{end}}
<div class="meta">当前查看 evt-{{printf "%06d" .Seq}} / 最新 evt-{{printf "%06d" .MaxSeq}} ｜ 机器可读事件：<a href="/api/events">/api/events</a></div>
</section>
</body></html>`

const byteHTML = `<!doctype html>
<html lang="zh"><head><meta charset="utf-8"><title>字节 {{.Byte.ID}}</title>
<style>
body{background:#0d1117;color:#c9d1d9;font-family:system-ui,sans-serif;margin:0;padding:16px}
table{border-collapse:collapse;font-size:12px}
td,th{border:1px solid #21262d;padding:3px 6px;text-align:left}
a{color:#58a6ff}.meta{font-family:monospace;font-size:12px;color:#8b949e}
</style></head><body>
<h1>字节 {{.Byte.ID}}</h1>
<div class="meta"><a href="/?seq={{.State.Seq}}">← 返回复核页（evt-{{printf "%06d" .State.Seq}}）</a></div>
<h2>值与归属</h2>
<table>
<tr><th>稳定身份</th><td>{{.Byte.ID}}</td></tr>
<tr><th>值</th><td>{{hex .Byte.Value}}（{{.Byte.Bits}} 位{{if .Byte.Ambig}}，含歧义位{{end}}）</td></tr>
<tr><th>角色</th><td>{{.Byte.Role}}</td></tr>
<tr><th>ACK/NACK</th><td>{{ack .Byte}}</td></tr>
<tr><th>时间区间</th><td>{{.Byte.StartNs}}..{{.Byte.EndNs}} ns（{{us .Byte.StartNs}} 起，时间基准 ns）</td></tr>
<tr><th>交易</th><td>{{.Txn.ID}} ｜ {{.Txn.Protocol}} ｜ 设备/片选 {{.Txn.Device}} ｜ 状态 {{.Txn.Status}}</td></tr>
<tr><th>候选</th><td>{{.Cand.ID}} ｜ {{.Cand.Decoder}} ｜ 置信 {{pct .Cand.Confidence}} ｜ {{.Cand.Reason}}</td></tr>
</table>
<h2>边沿回溯（任一字节可回到边沿）</h2>
<table><tr><th>通道</th><th>边沿序号</th><th>原始时间 (ns)</th></tr>
{{range .Byte.EdgeRefs}}<tr><td>{{.Channel}}</td><td>#{{.Index}}</td><td>{{.T}}</td></tr>{{end}}
</table>
<h2>来源关系（每个显示值都带稳定身份）</h2>
<div class="meta">
结果 {{.State.Result.ID}} ｜ 输入摘要 {{.State.Result.InputDigest}} ｜ 参数版本 v{{.State.Result.ParamVersion}}
｜ 解码器版本 {{.State.Result.DecoderVersion}} ｜ 父级依赖 {{range .State.Result.Parents}}{{.}} {{end}}<br>
{{with .State.Capture}}采集 {{.ID}} ｜ 采样率 {{.SampleRateHz}} Hz ｜ 有效区间 {{.Span}} ns{{end}}
</div>
<h2>波形（黄色 = 本字节位区间）</h2>
{{.Waveform}}
</body></html>`
