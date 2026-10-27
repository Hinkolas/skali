package build

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

// Insights is the report of one finished build: what the builder printed
// and how long each of its steps took. Whoever ran the build records it
// with an InsightsRecorder and hands it over once, after the build ended;
// build output is never streamed anywhere line by line.
type Insights struct {
	Outcome    string    `json:"outcome"`
	Engine     string    `json:"engine"`
	Platform   string    `json:"platform"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
	// Error is the build's failure, empty when it succeeded.
	Error string         `json:"error,omitempty"`
	Steps []InsightsStep `json:"steps"`
	// Log is the builder's output, one line per entry. A log over
	// MaxInsightsLogBytes keeps its head and tail around an omission
	// marker, and LogTruncated says so.
	Log          string `json:"log"`
	LogLines     int    `json:"log_lines"`
	LogTruncated bool   `json:"log_truncated"`
}

// InsightsStep is one builder step (a BuildKit vertex: a Dockerfile
// instruction, a context transfer, the export) in the order it started.
type InsightsStep struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	// Seconds is the step's duration as the builder reported it; cached
	// and incomplete steps report none.
	Seconds float64 `json:"seconds,omitempty"`
}

// Insights outcomes and step statuses.
const (
	InsightsSucceeded = "succeeded"
	InsightsFailed    = "failed"

	StepDone       = "done"
	StepCached     = "cached"
	StepError      = "error"
	StepCanceled   = "canceled"
	StepIncomplete = "incomplete"
)

// EngineDockerBuildx names the docker engine in provenance and insights.
const EngineDockerBuildx = "docker-buildx"

// Bounds of one insights report. The recorder keeps within them and the
// server refuses a report beyond them.
const (
	MaxInsightsLogBytes  = 2 << 20
	MaxInsightsSteps     = 1000
	MaxInsightsLineBytes = 8192
	maxInsightsHeadBytes = 256 << 10
	maxInsightsNameBytes = 512
	maxInsightsTextBytes = 8192
	maxInsightsTagBytes  = 128
	// maxInsightsMarkerBytes is the room the omission marker takes beyond
	// the log bound.
	maxInsightsMarkerBytes = 64
	// MaxInsightsJSONBytes bounds a report's JSON encoding: every bounded
	// text at its worst-case escaping (six bytes per byte, as in \u003c),
	// plus room for the rest.
	MaxInsightsJSONBytes = 6*(MaxInsightsLogBytes+maxInsightsMarkerBytes+MaxInsightsSteps*maxInsightsNameBytes+
		maxInsightsTextBytes+2*maxInsightsTagBytes) + 1<<20
)

// Validate checks a report against the insights bounds.
func (i Insights) Validate() error {
	switch i.Outcome {
	case InsightsSucceeded, InsightsFailed:
	default:
		return fmt.Errorf("outcome must be %s or %s", InsightsSucceeded, InsightsFailed)
	}
	if i.StartedAt.IsZero() || i.FinishedAt.IsZero() {
		return errors.New("started_at and finished_at are required")
	}
	if i.FinishedAt.Before(i.StartedAt) {
		return errors.New("finished_at precedes started_at")
	}
	if len(i.Engine) > maxInsightsTagBytes || len(i.Platform) > maxInsightsTagBytes {
		return errors.New("engine and platform must be short identifiers")
	}
	if len(i.Error) > maxInsightsTextBytes {
		return fmt.Errorf("error exceeds %d bytes", maxInsightsTextBytes)
	}
	if len(i.Steps) > MaxInsightsSteps {
		return fmt.Errorf("at most %d steps", MaxInsightsSteps)
	}
	for _, step := range i.Steps {
		if step.Name == "" || len(step.Name) > maxInsightsNameBytes {
			return fmt.Errorf("step names must be 1 to %d bytes", maxInsightsNameBytes)
		}
		switch step.Status {
		case StepDone, StepCached, StepError, StepCanceled, StepIncomplete:
		default:
			return fmt.Errorf("step %q has an unknown status %q", step.Name, step.Status)
		}
		if step.Seconds < 0 {
			return fmt.Errorf("step %q has a negative duration", step.Name)
		}
	}
	if len(i.Log) > MaxInsightsLogBytes+maxInsightsMarkerBytes {
		return fmt.Errorf("log exceeds %d bytes", MaxInsightsLogBytes)
	}
	if i.LogLines < 0 {
		return errors.New("log_lines must not be negative")
	}
	return nil
}

// InsightsRecorder is a ProgressSink that records a build's insights. It
// keeps the log's head and as much of its tail as fits the bound, the end
// being where a failure explains itself. Safe for the engine's concurrent
// stdout and stderr streams.
type InsightsRecorder struct {
	engine, platform string
	started          time.Time
	now              func() time.Time

	mu        sync.Mutex
	head      []string
	headBytes int
	// tail[tailStart:] is the kept tail; dropped lines are compacted away
	// only once they fill half the slice, so a long log costs amortized
	// constant work per line.
	tail      []string
	tailStart int
	tailBytes int
	lines     int
	omitted   int
	steps     []*recordedStep
	vertices  map[int]*recordedStep
}

type recordedStep struct {
	name    string
	status  string
	seconds float64
}

// NewInsightsRecorder starts recording a build that starts now.
func NewInsightsRecorder(engine, platform string) *InsightsRecorder {
	return newInsightsRecorder(engine, platform, time.Now)
}

func newInsightsRecorder(engine, platform string, now func() time.Time) *InsightsRecorder {
	return &InsightsRecorder{engine: engine, platform: platform, started: now(), now: now,
		vertices: map[int]*recordedStep{}}
}

// BuildKit's plain progress: "#<vertex> <text>", where text is the
// vertex's name on its first line and later its output or its verdict.
var plainVertexLine = regexp.MustCompile(`^#(\d+) (.*)$`)

// Line implements ProgressSink.
func (r *InsightsRecorder) Line(_, message string) {
	line := cleanInsightsLine(message)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines++
	r.keep(line)
	r.observe(line)
}

func cleanInsightsLine(message string) string {
	line := strings.ToValidUTF8(ansi.Strip(message), "�")
	line = strings.TrimRight(line, "\r")
	if len(line) > MaxInsightsLineBytes {
		cut := MaxInsightsLineBytes
		for cut > 0 && !utf8.RuneStart(line[cut]) {
			cut--
		}
		line = line[:cut] + " … [line truncated]"
	}
	return line
}

// keep stores the line in the head while it has room, then in the tail,
// dropping the tail's oldest lines beyond the bound.
func (r *InsightsRecorder) keep(line string) {
	size := len(line) + 1
	if r.omitted == 0 && len(r.tail) == r.tailStart && r.headBytes+size <= maxInsightsHeadBytes {
		r.head = append(r.head, line)
		r.headBytes += size
		return
	}
	r.tail = append(r.tail, line)
	r.tailBytes += size
	budget := MaxInsightsLogBytes - r.headBytes
	for r.tailBytes > budget && r.tailStart < len(r.tail) {
		r.tailBytes -= len(r.tail[r.tailStart]) + 1
		r.tail[r.tailStart] = ""
		r.tailStart++
		r.omitted++
	}
	if r.tailStart > len(r.tail)/2 {
		r.tail = append(r.tail[:0], r.tail[r.tailStart:]...)
		r.tailStart = 0
	}
}

// observe follows the vertex lines. "#0 building with" opens a builder
// invocation; a retried invocation (the daemon-export fallback) replaces
// the steps of the one before.
func (r *InsightsRecorder) observe(line string) {
	match := plainVertexLine.FindStringSubmatch(line)
	if match == nil {
		return
	}
	id, err := strconv.Atoi(match[1])
	if err != nil {
		return
	}
	text := match[2]
	if id == 0 && strings.HasPrefix(text, "building with ") {
		r.steps = nil
		r.vertices = map[int]*recordedStep{}
		return
	}
	step, seen := r.vertices[id]
	if !seen {
		if len(r.steps) >= MaxInsightsSteps {
			return
		}
		name := text
		if len(name) > maxInsightsNameBytes {
			name = strings.ToValidUTF8(name[:maxInsightsNameBytes-len("…")], "") + "…"
		}
		step = &recordedStep{name: name, status: StepIncomplete}
		r.vertices[id] = step
		r.steps = append(r.steps, step)
		return
	}
	switch {
	case text == "CACHED":
		step.status = StepCached
	case strings.HasPrefix(text, "DONE "):
		step.status = StepDone
		if seconds, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimPrefix(text, "DONE "), "s"), 64); err == nil && seconds >= 0 {
			step.seconds = seconds
		}
	case strings.HasPrefix(text, "ERROR"):
		step.status = StepError
	case strings.HasPrefix(text, "CANCELED"):
		step.status = StepCanceled
	}
}

// Finish ends the recording: a nil err is a successful build.
func (r *InsightsRecorder) Finish(err error) Insights {
	r.mu.Lock()
	defer r.mu.Unlock()
	insights := Insights{
		Outcome:    InsightsSucceeded,
		Engine:     r.engine,
		Platform:   r.platform,
		StartedAt:  r.started,
		FinishedAt: r.now(),
		Steps:      make([]InsightsStep, 0, len(r.steps)),
		LogLines:   r.lines,
	}
	if insights.FinishedAt.Before(insights.StartedAt) {
		insights.FinishedAt = insights.StartedAt
	}
	if err != nil {
		insights.Outcome = InsightsFailed
		insights.Error = err.Error()
		if len(insights.Error) > maxInsightsTextBytes {
			insights.Error = strings.ToValidUTF8(insights.Error[:maxInsightsTextBytes-len("…")], "") + "…"
		}
	}
	for _, step := range r.steps {
		insights.Steps = append(insights.Steps, InsightsStep{Name: step.name, Status: step.status, Seconds: step.seconds})
	}
	lines := append([]string(nil), r.head...)
	if r.omitted > 0 {
		insights.LogTruncated = true
		lines = append(lines, omissionMarker(r.omitted))
	}
	lines = append(lines, r.tail[r.tailStart:]...)
	insights.Log = strings.Join(lines, "\n")
	return insights
}

func omissionMarker(omitted int) string {
	return fmt.Sprintf("… %d lines omitted …", omitted)
}
