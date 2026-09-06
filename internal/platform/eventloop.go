package platform

import (
	"context"
	"log"

	"golang.design/x/chann"
)

type Eventloop struct {
	eventHandler    EventloopHandler
	sequencerClient *SequencerClient
}

type EventloopHandler func(*Event) any

func NewEventloop(
	component string,
	componentId string,
	ingressCh *chann.Chann[*Event],
	eventHandler EventloopHandler,
) *Eventloop {
	sequencerClient := NewSequencerClient(
		component,
		componentId,
		ingressCh,
	)
	return &Eventloop{
		sequencerClient: sequencerClient,
		eventHandler:    eventHandler,
	}
}

func (e *Eventloop) Run(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			e.sequencerClient.Close()
			log.Println("Eventloop shutting down")
			return
		}
		event := e.sequencerClient.Read(ctx)
		if event == nil {
			return
		}
		if payload := e.eventHandler(event); payload != nil {
			e.sequencerClient.Send(payload)
		}
	}
}

// SendExternalPayload Send payload generated outside of the eventloop, for example as the very first event for the system
func (e *Eventloop) SendExternalPayload(payload any) {
	e.sequencerClient.Send(payload)
}
