package kilo

import (
	"bytes"
	"context"
	"sync"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"
)

var pasteStart = []byte("\x1b[200~")
var pasteEnd = []byte("\x1b[201~")

// Normal keys use the maintained decoder. Clipboard bytes cross a separate
// boundary before that decoder can interpret CSI, Alt, DEL or invalid UTF-8.
type rawInput struct {
	ctx               context.Context
	events            chan tcell.Event
	keys              tcell.InputProcessor
	decodeMu          sync.Mutex
	prefix            []byte
	prefixAt          time.Time
	prefixEscSent     bool
	ordinary          []byte
	mu                sync.Mutex
	captured          []byte
	pasting, complete bool
	ack               chan struct{}
	fault             error
}

type rawPasteEvent struct {
	data []byte
	when time.Time
	ack  func()
}

func (e *rawPasteEvent) When() time.Time { return e.when }

func newRawInput(ctx context.Context) *rawInput {
	events := make(chan tcell.Event, 256)
	return &rawInput{ctx: ctx, events: events, keys: tcell.NewInputProcessor(events)}
}

func (p *rawInput) feed(data []byte) bool {
	p.decodeMu.Lock()
	defer p.decodeMu.Unlock()
	for _, b := range data {
		if p.ctx.Err() != nil {
			return false
		}
		p.mu.Lock()
		if p.pasting {
			p.captured = append(p.captured, b)
			complete := bytes.HasSuffix(p.captured, pasteEnd)
			// A suffix may still become the closing delimiter. Count only
			// definite source bytes until that delimiter is complete.
			pending := len(pasteEnd)
			for pending > 0 && !bytes.HasSuffix(p.captured, pasteEnd[:pending]) {
				pending--
			}
			if len(p.captured)-pending > maxPasteBytes {
				p.captured = p.captured[:maxPasteBytes+1]
				p.fault = ErrPasteTooLarge
				p.mu.Unlock()
				return false
			}
			if !complete {
				p.mu.Unlock()
				continue
			}
			p.captured = p.captured[:len(p.captured)-len(pasteEnd)]
			p.pasting, p.complete = false, true
			captured, ack := append([]byte{}, p.captured...), p.ack
			p.mu.Unlock()
			event := &rawPasteEvent{data: captured, when: time.Now(), ack: p.accept}
			select {
			case p.events <- event:
			case <-p.ctx.Done():
				return false
			}
			// Backpressure keeps one pending clipboard stable for exact recovery.
			select {
			case <-ack:
			case <-p.ctx.Done():
				return false
			}
			continue
		}
		p.mu.Unlock()
		if len(p.prefix) == 0 {
			p.prefixAt = time.Now()
			p.prefixEscSent = false
		}
		p.prefix = append(p.prefix, b)
		for len(p.prefix) > 0 && !bytes.HasPrefix(pasteStart, p.prefix) {
			if len(p.prefix) > 1 && p.prefix[0] == 27 && p.prefix[1] == 27 {
				// Repeated Escape starts a new frame. Deliver the first in
				// order; the key decoder must not retain it across a paste.
				if !p.prefixEscSent {
					p.emitEscape()
				}
				p.prefixEscSent = false
			} else if p.prefixEscSent {
				// A late CSI still needs its introducer in the key decoder.
				// Other text follows an already delivered standalone Escape.
				if len(p.prefix) > 1 && p.prefix[1] == '[' {
					p.scanOrdinary(p.prefix[:1])
				}
				p.prefixEscSent = false
			} else {
				p.scanOrdinary(p.prefix[:1])
			}
			p.prefix = p.prefix[1:]
			if len(p.prefix) == 1 && p.prefix[0] == 27 {
				p.prefixAt = time.Now()
			}
		}
		if bytes.Equal(p.prefix, pasteStart) {
			// A fresh clipboard frame interrupts an incomplete non-paste
			// sequence. Its parser state cannot modify keys after the frame.
			p.keys = tcell.NewInputProcessor(p.events)
			p.prefix = nil
			p.prefixEscSent = false
			p.mu.Lock()
			p.pasting, p.complete = true, false
			p.captured = nil
			p.ack = make(chan struct{})
			p.mu.Unlock()
		}
	}
	return true
}

// A solitary Escape emits once, retaining its sentinel for a late header.
// Multi-byte header prefixes remain framed until completion or cancellation.
func (p *rawInput) idle(now time.Time) {
	p.decodeMu.Lock()
	defer p.decodeMu.Unlock()
	if p.ctx.Err() != nil {
		return
	}
	if len(p.prefix) == 1 && !p.prefixEscSent && now.Sub(p.prefixAt) >= 50*time.Millisecond {
		p.prefixEscSent = true
		p.emitEscape()
	}
}

func (p *rawInput) emitEscape() {
	select {
	case p.events <- tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone):
	case <-p.ctx.Done():
	}
}

func (p *rawInput) scanOrdinary(data []byte) {
	p.ordinary = append(p.ordinary, data...)
	for utf8.FullRune(p.ordinary) {
		r, size := utf8.DecodeRune(p.ordinary)
		if r == utf8.RuneError && size == 1 {
			p.keys.ScanUTF8(p.ordinary[:1])
		} else {
			// The dependency's UTF-8 scanner treats split sequences and U+FFFD
			// as individual bytes. Frame complete runes through its UTF-16 API.
			p.keys.ScanUTF16(utf16.Encode([]rune{r}))
			p.keys.ScanUTF8(nil)
		}
		p.ordinary = p.ordinary[size:]
	}
}
func (p *rawInput) failure() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.fault
}
func (p *rawInput) accept() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.captured = nil
	p.pasting, p.complete = false, false
	if p.ack != nil {
		close(p.ack)
		p.ack = nil
	}
}
func (p *rawInput) snapshot() ([]byte, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.pasting && !p.complete {
		return nil, false
	}
	return append([]byte{}, p.captured...), !p.complete
}
