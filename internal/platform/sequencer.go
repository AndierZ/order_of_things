package platform

import (
	"context"

	"golang.design/x/chann"
)
import "log"

type Sequencer struct {
	subCh        *chann.Chann[*SequencerClient]
	ingressCh    *chann.Chann[*Event]
	clients      []*SequencerClient
	eventLog     []*Event
	sequence     int64
	senderSeqHwm map[string]int64
}

func NewSequencer() *Sequencer {
	return &Sequencer{
		subCh:        chann.New[*SequencerClient](),
		ingressCh:    chann.New[*Event](),
		clients:      make([]*SequencerClient, 0),
		sequence:     0,
		senderSeqHwm: make(map[string]int64),
	}
}

func (s *Sequencer) Run(ctx context.Context) {
	log.Println("Sequencer started")
	for {
		select {
		case <-ctx.Done():
			log.Println("Sequencer exited")
			return
		case client := <-s.subCh.Out():
			s.clients = append(s.clients, client)
			// replay
			for _, event := range s.eventLog {
				client.onSequencerEvent(event)
			}
			client.onReplayComplete()
		case event := <-s.ingressCh.Out():
			if !s.isValid(event) {
				continue
			}
			s.eventLog = append(s.eventLog, event)
			// sequence
			event.Header.Seq = s.sequence
			s.sequence++

			// fanout
			closedClients := make(map[string]struct{})
			for _, client := range s.clients {
				if client.isClosed() {
					closedClients[client.senderComponentId] = struct{}{}
				} else {
					client.onSequencerEvent(event)
				}
			}

			// cleanup closed clients
			if len(closedClients) > 0 {
				remainingClients := make([]*SequencerClient, 0)
				for _, client := range s.clients {
					if _, ok := closedClients[client.senderComponentId]; !ok {
						remainingClients = append(remainingClients, client)
					}
				}

				s.clients = remainingClients
			}
		}
	}
}

func (s *Sequencer) isValid(event *Event) bool {
	hwm, ok := s.senderSeqHwm[event.Header.SenderComponent]
	if !ok || event.Header.SenderSeq == hwm+1 {
		s.senderSeqHwm[event.Header.SenderComponent] = event.Header.SenderSeq
		return true
	}
	if event.Header.SenderSeq > hwm+1 {
		log.Println("Received sender sequence with gap")
	}
	return false
}
