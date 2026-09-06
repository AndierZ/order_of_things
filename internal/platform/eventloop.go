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

// Eventloop is the common substrate every component runs on: read a sequenced
// event, apply it, emit at most one event in response.
type Eventloop struct {
	eventHandler    EventloopHandler
	sequencerClient *SequencerClient
	sequencer       *Sequencer
}

func NewEventloop(
	component string,
	componentId string,
	sequencer *Sequencer,
	eventHandler EventloopHandler,
) *Eventloop {
	return &Eventloop{
		sequencerClient: NewSequencerClient(component, componentId, sequencer),
		eventHandler:    eventHandler,
		sequencer:       sequencer,
	}
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
		if payload := e.eventHandler(event); payload != nil {
			e.sequencerClient.Send(payload)
		}
	}
}

// IsQuarantined reports whether this replica removed itself after divergence.
func (e *Eventloop) IsQuarantined() bool {
	return e.sequencerClient.IsQuarantined()
}
