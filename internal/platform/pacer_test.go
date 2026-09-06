package platform

import (
	"context"
	"testing"
	"time"
)

func TestPacerRunsFlatOutAtZeroInterval(t *testing.T) {
	pacer := NewPacer(0)
	ctx := context.Background()
	start := time.Now()
	for i := 0; i < 1000; i++ {
		if !pacer.Wait(ctx) {
			t.Fatalf("Wait returned false at %d", i)
		}
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("1000 unpaced admissions took %v", elapsed)
	}
}

func TestPacerThrottles(t *testing.T) {
	pacer := NewPacer(20 * time.Millisecond)
	ctx := context.Background()

	start := time.Now()
	for i := 0; i < 3; i++ {
		if !pacer.Wait(ctx) {
			t.Fatalf("Wait returned false at %d", i)
		}
	}
	if elapsed := time.Since(start); elapsed < 50*time.Millisecond {
		t.Errorf("3 admissions at 20ms took only %v", elapsed)
	}
}

func TestPacerPauseBlocksUntilResume(t *testing.T) {
	pacer := NewPacer(0)
	ctx := context.Background()
	pacer.Pause()

	admitted := make(chan bool, 1)
	go func() { admitted <- pacer.Wait(ctx) }()

	select {
	case <-admitted:
		t.Fatal("Wait returned while paused")
	case <-time.After(50 * time.Millisecond):
	}

	pacer.Resume()
	select {
	case ok := <-admitted:
		if !ok {
			t.Error("Wait returned false after Resume")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Wait did not return after Resume")
	}
}

func TestPacerStepAdmitsExactlyOne(t *testing.T) {
	pacer := NewPacer(time.Hour) // slow enough that only Step can release anything
	ctx := context.Background()
	pacer.Pause()

	admitted := make(chan bool, 4)
	for i := 0; i < 2; i++ {
		go func() { admitted <- pacer.Wait(ctx) }()
	}

	pacer.Step()
	select {
	case <-admitted:
	case <-time.After(2 * time.Second):
		t.Fatal("Step did not admit an event")
	}
	select {
	case <-admitted:
		t.Fatal("Step admitted more than one event")
	case <-time.After(50 * time.Millisecond):
	}

	// Step must not have disturbed the configured speed.
	if running, interval := pacer.State(); running || interval != time.Hour {
		t.Errorf("after Step: running=%v interval=%v, want paused at 1h", running, interval)
	}
	pacer.Step()
	select {
	case <-admitted:
	case <-time.After(2 * time.Second):
		t.Fatal("a second Step did not admit an event")
	}
}

func TestPacerSpeedChangeInterruptsAWaitInProgress(t *testing.T) {
	pacer := NewPacer(time.Hour)
	ctx := context.Background()

	admitted := make(chan bool, 1)
	go func() { admitted <- pacer.Wait(ctx) }()

	// Give the waiter time to enter its hour-long timer, then speed it up.
	time.Sleep(20 * time.Millisecond)
	pacer.SetInterval(time.Millisecond)

	select {
	case ok := <-admitted:
		if !ok {
			t.Error("Wait returned false after a speed change")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a speed change did not interrupt the wait in progress")
	}
}

func TestPacerWaitReportsCancellation(t *testing.T) {
	pacer := NewPacer(time.Hour)
	ctx, cancel := context.WithCancel(context.Background())

	admitted := make(chan bool, 1)
	go func() { admitted <- pacer.Wait(ctx) }()
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case ok := <-admitted:
		if ok {
			t.Error("Wait returned true after cancellation")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Wait did not return after cancellation")
	}

	// And it must not block once cancelled, paused or not.
	pacer.Pause()
	if pacer.Wait(ctx) {
		t.Error("Wait returned true on a cancelled context")
	}
}

// Pacing changes when events are admitted, never which or in what order. Two
// runs of the same seed at different speeds must produce the same log.
func TestPacingDoesNotChangeTheLog(t *testing.T) {
	fast := NewPacer(0)
	slow := NewPacer(2 * time.Millisecond)

	logs := make([][]string, 0, 2)
	for _, pacer := range []*Pacer{fast, slow} {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)

		s := NewSequencer()
		s.SetPacer(pacer)
		done := make(chan struct{})
		go func() {
			defer close(done)
			s.Run(ctx)
		}()

		producer := NewSequencerClient("producer", "p1", s)
		join(t, ctx, s, producer, 0)
		for i := 0; i < 25; i++ {
			producer.Send(i)
			mustRead(t, ctx, producer)
		}
		cancel()
		<-done

		entries := make([]string, 0, 25)
		for _, e := range s.EventLog() {
			entries = append(entries, string(rune('a'+e.Header.Seq))+":"+string(rune('0'+e.Payload.(int)%10)))
		}
		logs = append(logs, entries)
	}
	if len(logs[0]) != 25 || len(logs[1]) != 25 {
		t.Fatalf("logs have %d and %d events, want 25 each", len(logs[0]), len(logs[1]))
	}
	for i := range logs[0] {
		if logs[0][i] != logs[1][i] {
			t.Fatalf("position %d: unpaced %q, paced %q", i, logs[0][i], logs[1][i])
		}
	}
}
