package main

import (
	"image"
	"image/color"
	"log"
	"math"
	"os"
	"runtime"
	"time"

	"gioui.org/app"
	"gioui.org/f32"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
)

type Stroke struct {
	Pts   []f32.Point
	Col   color.NRGBA
	Width float32 // px
}

type Annotator struct {
	keyTag struct{}
	ptrTag struct{}

	strokes []Stroke
	cur     *Stroke

	col       color.NRGBA
	widthDp   float32
	dim       bool
	debug     bool
	lastLogAt time.Time

	x11Ready bool
}

func main() {
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)
	debug := os.Getenv("ANNOTATOR_DEBUG") == "1" || os.Getenv("ANNOTATOR_DEBUG") == "true"
	log.Printf("starting gio-screenpen (go=%s os=%s debug=%v)", runtime.Version(), runtime.GOOS+"/"+runtime.GOARCH, debug)

	go func() {
		w := new(app.Window)
		w.Option(
			app.Title("gio-screenpen"),
			app.Decorated(false),
			app.Fullscreen.Option(),
		)

		a := &Annotator{
			col:     color.NRGBA{R: 255, A: 255}, // red default
			widthDp: 6,
			debug:   debug,
		}

		var ops op.Ops
		for {
			switch e := w.Event().(type) {
			case app.DestroyEvent:
				log.Printf("destroy: %v", e.Err)
				return
			case app.X11ViewEvent:
				if !a.x11Ready && e.Valid() {
					if err := x11MoveWindowToPointer(e.Display, e.Window); err != nil {
						if a.debug {
							log.Printf("x11 move-to-pointer failed: %v", err)
						}
					} else if a.debug {
						log.Printf("x11 moved window to pointer monitor (win=0x%x)", e.Window)
					}
					a.x11Ready = true
				}
			case app.FrameEvent:
				gtx := app.NewContext(&ops, e)
				a.frame(gtx)
				e.Frame(gtx.Ops)
			}
		}
	}()
	app.Main()
}

func (a *Annotator) frame(gtx layout.Context) {
	// Pointer events should be scoped to the window rect.
	area := clip.Rect{Max: gtx.Constraints.Max}.Push(gtx.Ops)
	event.Op(gtx.Ops, &a.ptrTag)
	area.Pop()

	// Keyboard focus/events.
	event.Op(gtx.Ops, &a.keyTag)
	key.InputHintOp{Tag: &a.keyTag, Hint: key.HintAny}.Add(gtx.Ops)
	gtx.Execute(key.FocusCmd{Tag: &a.keyTag})

	a.handlePointer(gtx)
	a.handleKeys(gtx)

	// Background.
	paint.FillShape(gtx.Ops, color.NRGBA{R: 245, G: 245, B: 245, A: 255}, clip.Rect{Max: gtx.Constraints.Max}.Op())
	if a.dim {
		paint.FillShape(gtx.Ops, color.NRGBA{A: 120}, clip.Rect{Max: gtx.Constraints.Max}.Op())
	}

	// Draw strokes.
	for i := range a.strokes {
		drawStroke(gtx.Ops, &a.strokes[i])
	}
	if a.cur != nil {
		drawStroke(gtx.Ops, a.cur)
	}
}

func (a *Annotator) handlePointer(gtx layout.Context) {
	for {
		ev, ok := gtx.Event(pointer.Filter{
			Target: &a.ptrTag,
			Kinds:  pointer.Press | pointer.Drag | pointer.Release | pointer.Cancel,
		})
		if !ok {
			break
		}
		pe := ev.(pointer.Event)
		if a.debug && time.Since(a.lastLogAt) > 150*time.Millisecond {
			log.Printf("pointer: kind=%v pos=(%.1f,%.1f) buttons=%v", pe.Kind, pe.Position.X, pe.Position.Y, pe.Buttons)
			a.lastLogAt = time.Now()
		}
		switch pe.Kind {
		case pointer.Press:
			if pe.Buttons&pointer.ButtonPrimary == 0 {
				continue
			}
			a.cur = &Stroke{Col: a.col, Width: dpToPx(gtx, a.widthDp)}
			a.cur.Pts = append(a.cur.Pts, pe.Position)
		case pointer.Drag:
			if a.cur == nil {
				continue
			}
			// Interpolate points so the line looks continuous (not dotted).
			last := a.cur.Pts[len(a.cur.Pts)-1]
			appendInterpolated(&a.cur.Pts, last, pe.Position, a.cur.Width/2)
		case pointer.Release, pointer.Cancel:
			if a.cur != nil {
				a.strokes = append(a.strokes, *a.cur)
				a.cur = nil
			}
		}
	}

	// Keep animating while drawing.
	if a.cur != nil {
		gtx.Execute(op.InvalidateCmd{})
	}
}

func (a *Annotator) handleKeys(gtx layout.Context) {
	// Log focus changes (and enable IME hints).
	for {
		ev, ok := gtx.Event(key.FocusFilter{Target: &a.keyTag})
		if !ok {
			break
		}
		if fe, ok := ev.(key.FocusEvent); ok && a.debug {
			log.Printf("key focus: %v", fe.Focus)
		}
	}

	for {
		ev, ok := gtx.Event(key.Filter{Focus: &a.keyTag, Name: ""})
		if !ok {
			break
		}
		ke := ev.(key.Event)
		if ke.State != key.Press {
			continue
		}
		if a.debug {
			log.Printf("key: name=%q mods=%v", ke.Name, ke.Modifiers)
		}
		switch ke.Name {
		case "R":
			a.col = color.NRGBA{R: 255, A: 255}
		case "G":
			a.col = color.NRGBA{G: 255, A: 255}
		case "B":
			a.col = color.NRGBA{B: 255, A: 255}
		case "Y":
			a.col = color.NRGBA{R: 255, G: 255, A: 255}
		case "O":
			a.col = color.NRGBA{R: 255, G: 165, A: 255}
		case "P":
			a.col = color.NRGBA{R: 255, G: 105, B: 180, A: 255}
		case "X":
			// "Blur" pen: wide semi-transparent black.
			a.col = color.NRGBA{A: 0x40}
			a.widthDp = 20
		case "1":
			a.widthDp = 3
		case "2":
			a.widthDp = 6
		case "3":
			a.widthDp = 12
		case "A":
			a.dim = !a.dim
		case "C":
			a.strokes = nil
			a.cur = nil
		case key.NameEscape:
			os.Exit(0)
		}
		gtx.Execute(op.InvalidateCmd{})
	}
}



func dpToPx(gtx layout.Context, dp float32) float32 {
	return float32(gtx.Metric.PxPerDp) * dp
}

func appendInterpolated(dst *[]f32.Point, a, b f32.Point, spacing float32) {
	if spacing <= 1 {
		*dst = append(*dst, b)
		return
	}
	dx := float64(b.X - a.X)
	dy := float64(b.Y - a.Y)
	d := math.Hypot(dx, dy)
	if d == 0 {
		return
	}
	steps := int(d / float64(spacing))
	if steps < 1 {
		*dst = append(*dst, b)
		return
	}
	for i := 1; i <= steps; i++ {
		t := float64(i) / float64(steps)
		p := f32.Point{
			X: float32(float64(a.X) + dx*t),
			Y: float32(float64(a.Y) + dy*t),
		}
		*dst = append(*dst, p)
	}
}

func drawStroke(ops *op.Ops, s *Stroke) {
	if len(s.Pts) == 0 {
		return
	}
	r := int(math.Max(1, float64(s.Width/2)))
	for _, p := range s.Pts {
		rect := image.Rect(int(p.X)-r, int(p.Y)-r, int(p.X)+r, int(p.Y)+r)
		paint.FillShape(ops, s.Col, clip.Ellipse(rect).Op(ops))
	}
}