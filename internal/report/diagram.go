package report

import (
	"fmt"
	stdhtml "html"
	"html/template"
	"net/url"
	"strings"

	"github.com/Wali05/recoverylab/internal/audit"
	"github.com/Wali05/recoverylab/internal/engine"
)

// A sequence diagram of one run: the client on the left, the service on the
// right, and every request, reply, kill, and state reading in the order it
// happened. It is drawn from the run's own record, so it shows what
// RecoveryLab saw rather than what the scenario is supposed to look like.

type seqKind int

const (
	seqRequest seqKind = iota // client → service
	seqBurst                  // several requests at once
	seqReply                  // service → client
	seqLost                   // a reply that never reached the client
	seqNoReply                // the connection died with nothing sent back
	seqKill                   // the service process is killed
	seqRestart                // the service process comes back
	seqState                  // a reading of the service's state
)

type seqStep struct {
	kind  seqKind
	label string
	note  string
	tone  string // "", "good", "bad", "fault", or "muted"
}

const (
	seqWidth   = 640
	seqClient  = 96
	seqService = 500
	seqRow     = 42
	seqTop     = 34
)

// diagram renders the steps of a run as inline SVG. Every piece of text that
// comes from the run is escaped here, which is why the result is safe to
// mark as HTML.
func diagram(a audit.Report, r engine.Report) template.HTML {
	steps := sequence(a, r)
	if len(steps) == 0 {
		return ""
	}
	height := seqTop + len(steps)*seqRow + 10
	var b strings.Builder
	fmt.Fprintf(&b, `<svg class="seq" viewBox="0 0 %d %d" role="img" aria-label="%s">`, seqWidth, height, stdhtml.EscapeString(describe(steps)))
	fmt.Fprintf(&b, `<text class="sq-head" x="%d" y="14" text-anchor="middle">Client</text>`, seqClient)
	fmt.Fprintf(&b, `<text class="sq-head" x="%d" y="14" text-anchor="middle">Your service</text>`, seqService)

	// The service's lifeline breaks while the process is dead.
	fmt.Fprintf(&b, `<line class="sq-lane" x1="%d" y1="24" x2="%d" y2="%d"/>`, seqClient, seqClient, height-4)
	from := 24
	for i, step := range steps {
		y := seqTop + i*seqRow + 20
		switch step.kind {
		case seqKill:
			fmt.Fprintf(&b, `<line class="sq-lane" x1="%d" y1="%d" x2="%d" y2="%d"/>`, seqService, from, seqService, y)
			from = -1
		case seqRestart:
			from = y
		}
	}
	if from >= 0 {
		fmt.Fprintf(&b, `<line class="sq-lane" x1="%d" y1="%d" x2="%d" y2="%d"/>`, seqService, from, seqService, height-4)
	}

	mid := (seqClient + seqService) / 2
	for i, step := range steps {
		y := seqTop + i*seqRow + 20
		tone := ""
		if step.tone != "" {
			tone = " " + step.tone
		}
		text := func(x int, anchor, class, value string, dy int) {
			if value != "" {
				fmt.Fprintf(&b, `<text class="%s%s" x="%d" y="%d" text-anchor="%s">%s</text>`, class, tone, x, y+dy, anchor, stdhtml.EscapeString(value))
			}
		}
		switch step.kind {
		case seqRequest:
			arrow(&b, seqClient, seqService, y, tone)
			text(mid, "middle", "sq-text", step.label, -8)
			text(mid, "middle", "sq-note", step.note, 15)
		case seqBurst:
			for _, dy := range []int{-5, 0, 5} {
				arrow(&b, seqClient, seqService, y+dy, tone)
			}
			text(mid, "middle", "sq-text", step.label, -13)
			text(mid, "middle", "sq-note", step.note, 20)
		case seqReply:
			arrow(&b, seqService, seqClient, y, tone)
			text(mid, "middle", "sq-text", step.label, -8)
			text(mid, "middle", "sq-note", step.note, 15)
		case seqLost:
			// The reply leaves the service and stops half-way.
			stop := mid - 30
			fmt.Fprintf(&b, `<line class="sq-arrow%s" x1="%d" y1="%d" x2="%d" y2="%d" stroke-dasharray="5 4"/>`, tone, seqService, y, stop+9, y)
			cross(&b, stop, y, tone)
			text((seqService+stop)/2+8, "middle", "sq-text", step.label, -8)
			text(stop-12, "end", "sq-note", step.note, 4)
		case seqNoReply:
			stop := seqClient + 26
			fmt.Fprintf(&b, `<line class="sq-arrow%s" x1="%d" y1="%d" x2="%d" y2="%d" stroke-dasharray="2 5"/>`, tone, seqService-26, y, stop+9, y)
			cross(&b, stop, y, tone)
			text(mid, "middle", "sq-note", step.label, -8)
		case seqKill:
			fmt.Fprintf(&b, `<circle class="sq-dot%s" cx="%d" cy="%d" r="9"/>`, tone, seqService, y)
			fmt.Fprintf(&b, `<path class="sq-mark" d="M%d %dl8 8m0 -8l-8 8"/>`, seqService-4, y-4)
			text(seqService-18, "end", "sq-text", step.label, -2)
			text(seqService-18, "end", "sq-note", step.note, 13)
		case seqRestart:
			fmt.Fprintf(&b, `<circle class="sq-ring%s" cx="%d" cy="%d" r="5"/>`, tone, seqService, y)
			text(seqService-18, "end", "sq-note", step.label, 4)
		case seqState:
			width := 16 + int(7.3*float64(len([]rune(step.label))))
			x := min(seqService-width/2, seqWidth-width-4)
			fmt.Fprintf(&b, `<rect class="sq-pill%s" x="%d" y="%d" width="%d" height="22" rx="11"/>`, tone, x, y-11, width)
			fmt.Fprintf(&b, `<text class="sq-text%s" x="%d" y="%d" text-anchor="middle">%s</text>`, tone, x+width/2, y+4, stdhtml.EscapeString(step.label))
			text(x-10, "end", "sq-note", step.note, 4)
		}
	}
	b.WriteString(`</svg>`)
	return template.HTML(b.String())
}

func arrow(b *strings.Builder, from, to, y int, tone string) {
	dir := 1
	if to < from {
		dir = -1
	}
	fmt.Fprintf(b, `<line class="sq-arrow%s" x1="%d" y1="%d" x2="%d" y2="%d"/>`, tone, from, y, to-dir*7, y)
	fmt.Fprintf(b, `<path class="sq-tip%s" d="M%d %dl%d -4.5v9z"/>`, tone, to, y, -dir*9)
}

func cross(b *strings.Builder, x, y int, tone string) {
	fmt.Fprintf(b, `<path class="sq-cross%s" d="M%d %dl10 10m0 -10l-10 10"/>`, tone, x-5, y-5)
}

// sequence turns a run into diagram steps. It only uses what the run
// recorded: if an experiment stopped early, the diagram stops there too.
func sequence(a audit.Report, r engine.Report) []seqStep {
	request := strings.TrimSpace(a.Method + " " + shortPath(a.URL))
	attempt := func(kind string) (engine.Attempt, bool) {
		for _, at := range r.Attempts {
			if at.Kind == kind {
				return at, true
			}
		}
		return engine.Attempt{}, false
	}
	verdict := func(ok bool) string {
		if ok {
			return "good"
		}
		return "bad"
	}
	var steps []seqStep
	add := func(kind seqKind, label, note, tone string) {
		steps = append(steps, seqStep{kind, label, note, tone})
	}
	states := func(checks []engine.StateCheck, note string) {
		for i, c := range checks {
			if c.Observed == nil {
				continue
			}
			if i == 3 && len(checks) > 4 {
				add(seqState, fmt.Sprintf("and %d more checks", len(checks)-i), "", "muted")
				break
			}
			label := fmt.Sprintf("%s %d → %d", c.Name, c.Baseline, *c.Observed)
			if !c.Held() {
				label += fmt.Sprintf(", expected %d", c.Expected)
			}
			add(seqState, label, note, verdict(c.Held()))
			note = ""
		}
	}
	retry := func(note string) {
		at, ok := attempt("retry")
		if !ok {
			return
		}
		add(seqRequest, "same request again", note, "")
		if at.ClientError != "" {
			add(seqNoReply, "no reply", "", "bad")
			return
		}
		passed := r.RetryResponse == nil || r.RetryResponse.Passed
		reason := ""
		if rr := r.RetryResponse; rr != nil && !rr.Passed {
			reason = "expected " + rr.ExpectedStatus
			if rr.RetryHTTPStatus >= 200 && rr.RetryHTTPStatus < 300 && len(rr.SameJSONPointers) > 0 {
				reason = "different " + strings.Join(rr.SameJSONPointers, ", ")
			}
		}
		add(seqReply, fmt.Sprintf("HTTP %d", at.HTTPStatus), reason, verdict(passed))
	}

	switch r.Scenario {
	case "lost-response":
		original, ok := attempt("original")
		if !ok {
			return nil
		}
		add(seqRequest, request, "", "")
		if !r.FaultInjected {
			if original.HTTPStatus != 0 {
				add(seqReply, fmt.Sprintf("HTTP %d", original.HTTPStatus), "", "bad")
			}
			return steps
		}
		label := "reply"
		if rr := r.RetryResponse; rr != nil && rr.OriginalHTTPStatus != 0 {
			label = fmt.Sprintf("HTTP %d", rr.OriginalHTTPStatus)
		}
		add(seqLost, label, "reply lost on the way", "fault")
		retry("")
		states(r.Checks, "")

	case "concurrent-duplicates":
		if len(r.Attempts) == 0 {
			return nil
		}
		add(seqBurst, fmt.Sprintf("%d × %s", len(r.Attempts), request), "released together", "")
		passed := r.ConcurrentResponse == nil || r.ConcurrentResponse.Passed
		add(seqReply, strings.Join(tally(r.Attempts), ", "), "", verdict(passed))
		states(r.Checks, "")

	case "key-reuse":
		original, ok := attempt("original")
		if !ok {
			return nil
		}
		add(seqRequest, request, "", "")
		if original.ClientError != "" {
			add(seqNoReply, "no reply", "", "bad")
			return steps
		}
		add(seqReply, fmt.Sprintf("HTTP %d", original.HTTPStatus), "", verdict(original.HTTPStatus >= 200 && original.HTTPStatus < 300))
		changed, ok := attempt("changed-payload")
		if !ok {
			return steps
		}
		add(seqRequest, "same key, different body", "", "fault")
		if changed.ClientError != "" {
			add(seqNoReply, "no reply", "", "bad")
			return steps
		}
		note := ""
		if kr := r.KeyReuseResponse; kr != nil && !kr.Passed {
			note = fmt.Sprintf("expected HTTP %d", kr.ExpectedHTTPStatus)
		}
		add(seqReply, fmt.Sprintf("HTTP %d", changed.HTTPStatus), note, verdict(r.KeyReuseResponse == nil || r.KeyReuseResponse.Passed))
		states(r.Checks, "")

	case "crash-after-ack":
		original, ok := attempt("original")
		if !ok {
			return nil
		}
		add(seqRequest, request, "", "")
		if original.ClientError != "" {
			add(seqNoReply, "no reply", "", "bad")
			return steps
		}
		add(seqReply, fmt.Sprintf("HTTP %d", original.HTTPStatus), "", verdict(original.HTTPStatus >= 200 && original.HTTPStatus < 300))
		if !r.FaultInjected {
			return steps
		}
		add(seqKill, "process killed", "right after the reply", "fault")
		if !restarted(r) {
			return steps
		}
		add(seqRestart, "restarted", "", "")
		states(r.AfterRestartChecks, "before the retry")
		retry("")
		states(r.Checks, "")

	case "crash-mid-request":
		original, ok := attempt("original")
		if !ok {
			return nil
		}
		add(seqRequest, request, "", "")
		cp := r.CrashPoint
		if cp == nil {
			if original.ClientError == "" && original.HTTPStatus != 0 {
				add(seqReply, fmt.Sprintf("HTTP %d", original.HTTPStatus), "", "bad")
			}
			return steps
		}
		when := fmt.Sprintf("%s ms after the request", trimFloat(cp.DelayMS))
		if cp.ReplyReceived {
			add(seqReply, fmt.Sprintf("HTTP %d", original.HTTPStatus), "", "good")
			add(seqKill, "process killed", when, "fault")
		} else {
			add(seqKill, "process killed", when, "fault")
			add(seqNoReply, "connection died, no reply", "", "fault")
		}
		if !restarted(r) {
			return steps
		}
		add(seqRestart, "restarted", "", "")
		note := map[string]string{"applied": "write is all there", "not-applied": "nothing written yet", "partial": "half-finished", "unexpected": "neither before nor after"}[cp.Outcome]
		before := len(steps)
		states(r.AfterRestartChecks, note)
		if cp.Outcome == "partial" {
			// Each value is acceptable alone; together they are not.
			for i := before; i < len(steps); i++ {
				steps[i].tone = "bad"
			}
		}
		retry("")
		states(r.Checks, "")
	}
	return steps
}

func restarted(r engine.Report) bool {
	for _, e := range r.Events {
		if e.Step == "recovery" {
			return true
		}
	}
	return false
}

// shortPath keeps a request line short enough to sit on an arrow.
func shortPath(raw string) string {
	path := raw
	if u, err := url.Parse(raw); err == nil && u.Path != "" {
		path = u.Path
	}
	if runes := []rune(path); len(runes) > 34 {
		path = "…" + string(runes[len(runes)-33:])
	}
	return path
}

func trimFloat(v float64) string {
	if v >= 10 {
		return fmt.Sprintf("%.0f", v)
	}
	return strings.TrimSuffix(fmt.Sprintf("%.1f", v), ".0")
}

// describe is the diagram in words, for screen readers.
func describe(steps []seqStep) string {
	parts := make([]string, 0, len(steps))
	for _, s := range steps {
		text := s.label
		switch s.kind {
		case seqRequest, seqBurst:
			text = "client sends " + s.label
		case seqReply:
			text = "service replies " + s.label
		case seqLost:
			text = "service replies " + s.label + ", but the reply is lost"
		case seqState:
			text = "state: " + s.label
		}
		if s.note != "" && s.kind != seqLost {
			text += " (" + s.note + ")"
		}
		parts = append(parts, text)
	}
	return strings.Join(parts, "; ")
}
