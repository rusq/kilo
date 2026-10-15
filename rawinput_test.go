package kilo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
)

func rawEvent(t *testing.T, p *rawInput) tcell.Event {
	t.Helper()
	select {
	case event := <-p.events:
		return event
	case <-time.After(2 * time.Second):
		t.Fatal("raw input event did not arrive")
		return nil
	}
}

func TestRawClipboardBeforeDecoderAndSplitFrames(t *testing.T) {
	source := []byte("猫\r\nA\x1b[31mB\x1b[AQ\x1bxZ\x7f\xff\x03\x13\x11")
	frame := append(append(append([]byte{}, pasteStart...), source...), pasteEnd...)
	for split := 0; split <= len(frame); split++ {
		ctx, cancel := context.WithCancel(t.Context())
		p := newRawInput(ctx)
		done := make(chan bool, 1)
		go func() { done <- p.feed(frame[:split]) && p.feed(frame[split:]) }()
		event, ok := rawEvent(t, p).(*rawPasteEvent)
		if !ok || !bytes.Equal(event.data, source) {
			cancel()
			t.Fatalf("split %d lost clipboard bytes: %#v", split, event)
		}
		captured, partial := p.snapshot()
		if partial || !bytes.Equal(captured, source) {
			t.Fatal("pending complete frame changed", captured, partial)
		}
		event.ack()
		if !<-done {
			t.Fatal("acknowledged reader did not finish")
		}
		if captured, _ = p.snapshot(); captured != nil {
			t.Fatal("acknowledged clipboard remained pending")
		}
		cancel()
	}
}

func TestRawPartialAndOversizedClipboardRecovery(t *testing.T) {
	for _, source := range [][]byte{[]byte("A\x1b[31mB\x7f\xff\x1b[20"), bytes.Repeat([]byte("x"), maxPasteBytes+40)} {
		p := newRawInput(t.Context())
		p.feed(append(append([]byte{}, pasteStart...), source...))
		captured, partial := p.snapshot()
		if !partial {
			t.Fatal("unfinished clipboard is not partial")
		}
		want := source
		if len(want) > maxPasteBytes+1 {
			want = want[:maxPasteBytes+1]
			if p.failure() == nil {
				t.Fatal("oversized clipboard did not fail")
			}
		}
		if !bytes.Equal(captured, want) {
			t.Fatal("raw partial prefix changed")
		}
	}
}

func TestDelayedPasteHeadersRetainRawFraming(t *testing.T) {
	for split := 1; split < len(pasteStart); split++ {
		ctx, cancel := context.WithCancel(t.Context())
		p := newRawInput(ctx)
		p.feed(pasteStart[:split])
		p.idle(time.Now().Add(time.Second))
		p.idle(time.Now().Add(2 * time.Second))
		if split == 1 {
			if key := rawEvent(t, p).(*tcell.EventKey); key.Key() != tcell.KeyEscape {
				t.Fatal(key)
			}
		}
		if len(p.events) != 0 {
			t.Fatal("incomplete header emitted decoder events", split)
		}
		source := []byte("A\x1b[31mB\x7f\xff\x13\x11")
		tail := append(append(append([]byte{}, pasteStart[split:]...), source...), pasteEnd...)
		done := make(chan bool, 1)
		go func() { done <- p.feed(tail) }()
		event, ok := rawEvent(t, p).(*rawPasteEvent)
		if !ok || !bytes.Equal(event.data, source) {
			cancel()
			t.Fatal("late header escaped raw framing", split, event)
		}
		event.ack()
		if !<-done {
			t.Fatal("late header reader did not finish")
		}
		p.feed([]byte("X"))
		if key := rawEvent(t, p).(*tcell.EventKey); key.Rune() != 'X' {
			t.Fatal("late paste left decoder state", key)
		}
		cancel()
	}
}

func TestRepeatedEscapeAndLateOrdinaryCSI(t *testing.T) {
	p := newRawInput(t.Context())
	p.feed([]byte{27})
	p.idle(time.Now().Add(time.Second))
	if key := rawEvent(t, p).(*tcell.EventKey); key.Key() != tcell.KeyEscape {
		t.Fatal(key)
	}
	p.feed([]byte{27})
	p.idle(time.Now().Add(time.Second))
	if key := rawEvent(t, p).(*tcell.EventKey); key.Key() != tcell.KeyEscape {
		t.Fatal(key)
	}
	p.feed([]byte("[A"))
	if key := rawEvent(t, p).(*tcell.EventKey); key.Key() != tcell.KeyUp {
		t.Fatal("late ordinary CSI changed", key)
	}
	p.feed([]byte("Z"))
	if key := rawEvent(t, p).(*tcell.EventKey); key.Rune() != 'Z' {
		t.Fatal(key)
	}
}

func TestRapidEscapePasteKeepsOrderedSaveAndQuit(t *testing.T) {
	for _, prefix := range []string{"\x1b", "\x1b\x1b", "\x1b[1;", "\x1bO"} {
		for split := 0; split <= len(pasteStart); split++ {
			ctx, cancel := context.WithCancel(t.Context())
			p := newRawInput(ctx)
			done := make(chan bool, 1)
			go func() {
				done <- p.feed([]byte(prefix+string(pasteStart[:split]))) && p.feed([]byte(string(pasteStart[split:])+"body"+string(pasteEnd)+"\x13\x11"))
			}()
			seenPaste, seenSave, seenQuit := false, false, false
			for !seenQuit {
				switch event := rawEvent(t, p).(type) {
				case *rawPasteEvent:
					if string(event.data) != "body" || seenPaste || seenSave {
						t.Fatal("paste event/order changed", prefix, event)
					}
					seenPaste = true
					event.ack()
				case *tcell.EventKey:
					switch event.Key() {
					case tcell.KeyEscape:
						if seenPaste {
							t.Fatal("escape overtook paste", prefix)
						}
					case tcell.KeyCtrlS:
						if !seenPaste {
							t.Fatal("save preceded paste")
						}
						seenSave = true
					case tcell.KeyCtrlQ:
						if !seenSave {
							t.Fatal("quit preceded save")
						}
						seenQuit = true
					default:
						t.Fatal("pending parser state altered keys", prefix, event)
					}
				}
			}
			if !<-done {
				t.Fatal("reader did not finish")
			}
			cancel()
		}
	}
}

func TestOrdinaryKeysPreserveSplitUnicodeAndDecoderNavigation(t *testing.T) {
	p := newRawInput(t.Context())
	for _, b := range []byte("猫e\u0301�\x1b[A\x13") {
		p.feed([]byte{b})
	}
	for _, want := range []rune{'猫', 'e', '\u0301', '�'} {
		key, ok := rawEvent(t, p).(*tcell.EventKey)
		if !ok || key.Key() != tcell.KeyRune || key.Rune() != want {
			t.Fatalf("ordinary Unicode changed: %#v want %U", key, want)
		}
	}
	for _, want := range []tcell.Key{tcell.KeyUp, tcell.KeyCtrlS} {
		key, ok := rawEvent(t, p).(*tcell.EventKey)
		if !ok || key.Key() != want {
			t.Fatalf("normal key decoder changed: %#v want %v", key, want)
		}
	}
	p.feed([]byte{27})
	p.idle(time.Now().Add(time.Second))
	if key := rawEvent(t, p).(*tcell.EventKey); key.Key() != tcell.KeyEscape {
		t.Fatal("solitary escape was not decoded", key)
	}
}

type rawSimulationScreen struct {
	*readyScreen
	source *rawInput
	cancel context.CancelFunc
	joined chan struct{}
}

func (s *rawSimulationScreen) stopPasteInput() ([]byte, bool) {
	s.cancel()
	<-s.joined
	return s.source.snapshot()
}

func TestRawPasteEditorRecoveryAndFindIsolation(t *testing.T) {
	for _, find := range []bool{false, true} {
		for _, complete := range []bool{false, true} {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			p := newRawInput(ctx)
			path := filepath.Join(t.TempDir(), "note.md")
			os.WriteFile(path, []byte("old"), 0600)
			screen := &rawSimulationScreen{readyScreen: &readyScreen{SimulationScreen: tcell.NewSimulationScreen(""), ready: make(chan struct{})}, source: p, cancel: cancel, joined: make(chan struct{})}
			done := make(chan error, 1)
			go func() { done <- runScreen(ctx, path, screen) }()
			<-screen.ready
			if find {
				screen.PostEventWait(tcell.NewEventKey(tcell.KeyCtrlF, 0, tcell.ModNone))
			}
			source := "A\r\nB\x1b[31mZ\x7f\x13\x11"
			frame := append(append([]byte{}, pasteStart...), []byte(source)...)
			if complete {
				frame = append(frame, pasteEnd...)
			}
			go func() {
				defer close(screen.joined)
				p.feed(frame[:3])
				p.idle(time.Now().Add(time.Second))
				p.feed(frame[3:])
			}()
			if complete {
				event := rawEvent(t, p)
				screen.PostEventWait(event)
				// This ordered key cancels after the whole paste was consumed.
				screen.PostEventWait(tcell.NewEventKey(tcell.KeyCtrlC, 0, tcell.ModNone))
			} else {
				<-screen.joined
				// Find must enter its nested loop before cancellation.
				if find {
					deadline := time.Now().Add(2 * time.Second)
					for {
						_, height := screen.Size()
						r, _, _, _ := screen.GetContent(0, height-1)
						if r == 'S' {
							break
						}
						if time.Now().After(deadline) {
							t.Fatal("Find did not start")
						}
						time.Sleep(time.Millisecond)
					}
				}
				cancel()
			}
			var err error
			select {
			case err = <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("raw editor did not join")
			}
			cancel()
			if !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			if find {
				if _, ok := errors.AsType[*RecoveryError](err); ok {
					t.Fatal("query paste mutated note", err)
				}
			} else {
				recovered, ok := errors.AsType[*RecoveryError](err)
				if !ok || recovered.Partial != !complete {
					t.Fatal("wrong clipboard recovery", err)
				}
				data, readErr := os.ReadFile(recovered.Path)
				os.RemoveAll(filepath.Dir(recovered.Path))
				if readErr != nil || string(data) != normalizePaste(source)+"old" {
					t.Fatal("raw recovery changed bytes", string(data), readErr)
				}
			}
		}
	}
}

func TestRawPasteLimitBoundaries(t *testing.T) {
	for _, size := range []int{maxPasteBytes - 1, maxPasteBytes, maxPasteBytes + 1, maxPasteBytes + len(pasteEnd)} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			p := newRawInput(ctx)
			source := bytes.Repeat([]byte("x"), size)
			frame := append(append(append([]byte{}, pasteStart...), source...), pasteEnd...)
			done := make(chan bool, 1)
			go func() { done <- p.feed(frame) }()
			if size <= maxPasteBytes {
				event := rawEvent(t, p).(*rawPasteEvent)
				if !bytes.Equal(event.data, source) {
					t.Fatal("accepted paste changed")
				}
				event.ack()
				if !<-done || p.failure() != nil {
					t.Fatal("valid paste failed", p.failure())
				}
			} else {
				if <-done || !errors.Is(p.failure(), ErrPasteTooLarge) {
					t.Fatal("oversized paste accepted", p.failure())
				}
				captured, partial := p.snapshot()
				if !partial || !bytes.Equal(captured, source[:maxPasteBytes+1]) {
					t.Fatal("oversized recovery prefix changed")
				}
			}
		})
	}
}
