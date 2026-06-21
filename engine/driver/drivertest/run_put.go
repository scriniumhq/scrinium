package drivertest

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func runPut(t *testing.T, f Factory) {
	t.Run("RoundTrip", func(t *testing.T) {
		d := f.New(t)
		putBlob(t, d, "blob/x", "hello, scrinium")
		if got := getBlob(t, d, "blob/x"); got != "hello, scrinium" {
			t.Fatalf("got %q, want %q", got, "hello, scrinium")
		}
	})

	// A key with slashes round-trips; the driver creates whatever
	// intermediate structure the backend needs.
	t.Run("NestedKey", func(t *testing.T) {
		d := f.New(t)
		putBlob(t, d, "a/b/c/d.txt", "ok")
		if got := getBlob(t, d, "a/b/c/d.txt"); got != "ok" {
			t.Fatalf("got %q, want %q", got, "ok")
		}
	})

	t.Run("Overwrite", func(t *testing.T) {
		d := f.New(t)
		putBlob(t, d, "f", "first")
		putBlob(t, d, "f", "second")
		if got := getBlob(t, d, "f"); got != "second" {
			t.Fatalf("got %q, want %q", got, "second")
		}
	})

	// AtomicityUnderRead is the core safety guarantee: a Get running in
	// parallel with Put never observes a partially written object — it
	// sees either the previous content in full, os.ErrNotExist, or the
	// new content in full, never a mix.
	t.Run("AtomicityUnderRead", func(t *testing.T) {
		d := f.New(t)
		ctx := t.Context()

		oldPayload := bytes.Repeat([]byte("OLD"), 100)
		if err := d.Put(ctx, "live", bytes.NewReader(oldPayload)); err != nil {
			t.Fatal(err)
		}
		newPayload := bytes.Repeat([]byte("NEW"), 5000)

		var wg sync.WaitGroup
		var partial atomic.Bool

		// Readers: many parallel Get calls, each verifying the object is
		// either fully OLD or fully NEW.
		for i := 0; i < 40; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for j := 0; j < 50; j++ {
					r, err := d.Get(ctx, "live")
					if errors.Is(err, os.ErrNotExist) {
						continue
					}
					if err != nil {
						t.Errorf("Get: %v", err)
						return
					}
					data, err := io.ReadAll(r)
					r.Close()
					if err != nil {
						t.Errorf("ReadAll: %v", err)
						return
					}
					if !bytes.Equal(data, oldPayload) && !bytes.Equal(data, newPayload) {
						partial.Store(true)
						return
					}
				}
			}()
		}

		// Writer: a single Put overwriting the object.
		wg.Add(1)
		go func() {
			defer wg.Done()
			time.Sleep(2 * time.Millisecond) // let readers warm up
			if err := d.Put(ctx, "live", bytes.NewReader(newPayload)); err != nil {
				t.Errorf("Put: %v", err)
			}
		}()

		wg.Wait()

		if partial.Load() {
			t.Fatal("observed a partial read during Put — atomicity broken")
		}
	})

	// A cancelled context aborts the write with context.Canceled.
	t.Run("ContextCancelled", func(t *testing.T) {
		d := f.New(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := d.Put(ctx, "f", bytes.NewReader([]byte("x")))
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	})
}
