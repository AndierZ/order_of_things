package platform

import (
	"context"
	"log"
)

// EventloopHandler applies one sequenced event to the component's state machine
// and optionally returns a payload to emit in response.
//
// It must be a pure function of the events it has been given: no time.Now, no
// ungoverned randomness, no map-iteration-order dependence. Both replicas of a
// component run this same function over the same event history and are expected
// to produce byte-identical output.
//
// The handler is also invoked once with a ReplayComplete event, after the log has
// been replayed and before any live event. Components that need to bootstrap the
// stream (the game injector emitting the first game) do it there, because that is
// the first point at which an emission will actually be transmitted.
type EventloopHandler func(*Event) any

// StateValidator checks a component's state root against a reference after each
// event it applies. Returning an error quarantines the replica.
//
// This is the second, independent quarantine path. The one built into the
// sequencer client compares what a replica *emitted* against what the log
// recorded, which catches a corrupted decision function but says nothing about
// which of two disagreeing replicas is right. This one compares what a replica
// *computed* against a reference established before it ran, which catches a
// corrupted transition function and does say which side is wrong.
type StateValidator interface {
	Validate(seq int64, root uint64) error
}

// Eventloop is the common substrate every component runs on: read a sequenced
// event, apply it, emit at most one event in response.
type Eventloop struct {
	eventHandler    EventloopHandler
	sequencerClient *SequencerClient
	sequencer       *Sequencer

	stateRoot func() uint64
	validator StateValidator
}

// Option configures an event loop at construction.
type Option func(*Eventloop)

// WithStateValidation checks the component's state root against a reference
// after every applied event, including every event replayed at boot. Checking
// during replay is the point: a restarting replica is refused rejoin before it
// can serve, rather than after it has already answered something wrong.
func WithStateValidation(stateRoot func() uint64, validator StateValidator) Option {
	return func(e *Eventloop) {
		e.stateRoot, e.validator = stateRoot, validator
	}
}

func NewEventloop(
	component string,
	componentId string,
	sequencer *Sequencer,
	eventHandler EventloopHandler,
	opts ...Option,
) *Eventloop {
	e := &Eventloop{
		sequencerClient: NewSequencerClient(component, componentId, sequencer),
		eventHandler:    eventHandler,
		sequencer:       sequencer,
	}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// Run subscribes to the sequencer and processes events until ctx is cancelled or
// the replica quarantines itself. A *DivergenceError return means this replica
// disagreed with its sibling and has removed itself from the pair; the rest of
// the system carries on with the surviving replica.
func (e *Eventloop) Run(ctx context.Context) error {
	e.sequencer.Subscribe(e.sequencerClient)
	defer e.sequencerClient.Close()

	for {
		if ctx.Err() != nil {
			log.Println("Eventloop shutting down")
			return nil
		}
		event, err := e.sequencerClient.Read(ctx)
		if err != nil {
			log.Printf("Eventloop quarantined: %v", err)
			return err
		}
		if event == nil {
			return nil
		}
		payload := e.eventHandler(event)

		// Validate before emitting. A replica whose state has diverged must not
		// be allowed to put a decision derived from that state onto the stream.
		if err := e.validateState(event); err != nil {
			log.Printf("Eventloop quarantined: %v", err)
			e.sequencerClient.quarantined = true
			e.sequencerClient.Close()
			return err
		}
		if payload != nil {
			e.sequencerClient.Send(payload)
		}
	}
}

func (e *Eventloop) validateState(event *Event) error {
	if e.validator == nil || e.stateRoot == nil || event.IsReplayComplete() {
		return nil
	}
	return e.validator.Validate(event.Header.Seq, e.stateRoot())
}

// IsQuarantined reports whether this replica removed itself after divergence.
func (e *Eventloop) IsQuarantined() bool {
	return e.sequencerClient.IsQuarantined()
}
