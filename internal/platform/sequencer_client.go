package platform

import (
	"context"

	"golang.design/x/chann"
)

type SequencerClient struct {
	senderComponent   string
	senderComponentId string
	isActive          bool
	closed            bool
	senderSeq         int64
	inflightEvent     *Event
	ingressCh         *chann.Chann[*Event]
	egressCh          *chann.Chann[*Event]
}

func NewSequencerClient(
	senderComponent string,
	senderComponentId string,
	ingressCh *chann.Chann[*Event],
) *SequencerClient {
	return &SequencerClient{
		senderComponent:   senderComponent,
		senderComponentId: senderComponentId,
		isActive:          false,
		closed:            false,
		senderSeq:         0,
		ingressCh:         ingressCh,
		egressCh:          chann.New[*Event](),
	}
}

// Send payload passed by value
func (c *SequencerClient) Send(payload any) {
	// Application sends events to the sequencer
	// ingressCh.Out() is being consumed from the sequencer
	event := &Event{
		Header: Header{
			SenderComponent: c.senderComponent,
			SenderId:        c.senderComponentId,
			SenderSeq:       c.senderSeq,
		},
		// TODO: Make sure this is a copy of a value only struct for immutability
		Payload: payload,
	}
	c.senderSeq++
	c.inflightEvent = event
	// Do not send event during replay
	if c.isActive {
		// In real application event would go over the network
		c.ingressCh.In() <- event
	}
}

func (c *SequencerClient) onSequencerEvent(event *Event) {
	// Sequencer sends events to the application
	// In real application events would come from the network
	c.egressCh.In() <- event
}

func (c *SequencerClient) Read(ctx context.Context) *Event {
	// Application reads sequencer events
	select {
	case <-ctx.Done():
		return nil
	case event := <-c.egressCh.Out():
		if !c.eventMatchInflight(event) {
			c.closed = true
			panic("Divergence detected. Inflight event {}. Received event {}.")
		}
		return event
	}
}

func (c *SequencerClient) Close() {
	c.closed = true
}

func (c *SequencerClient) isClosed() bool {
	return c.closed
}

func (c *SequencerClient) onReplayComplete() {
	c.isActive = true
}

func (c *SequencerClient) eventMatchInflight(event *Event) bool {
	if c.inflightEvent == nil {
		return true
	}
	if event.Header.SenderComponent != c.senderComponent {
		return true
	}
	// TODO compare two structs with value only fields
	return c.inflightEvent.Payload == event.Payload
}
