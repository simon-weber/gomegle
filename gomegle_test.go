package gomegle

import (
	"fmt"
	"testing"
	"time"
)

/*
func TestUnitReadStart(t *testing.T) {
	start_raw := []byte(`{"events": [["waiting"], ["statusInfo", {"count": 36770, "antinudeservers": ["waw2.omegle.com", "waw3.omegle.com", "waw1.omegle.com"], "spyQueueTime": 9.9992752075199996e-05, "antinudepercent": 1.0, "spyeeQueueTime": 1.6791000127780003, "timestamp": 1371762232.1154649, "servers": ["front8.omegle.com", "front7.omegle.com", "front3.omegle.com", "front5.omegle.com", "front9.omegle.com", "front1.omegle.com", "front6.omegle.com", "front2.omegle.com", "front4.omegle.com"]}]], "clientID": "shard2:r2s8xwxq484rlb7c641u5dqu1tbzor"}`)
	events, err := parseResponse(start_raw)
	if err != nil {
		t.Fatal(err)
	}
	waiting := Event{Kind: "waiting"}
	gotId := Event{Kind: "gotId", Value: "shard2:r2s8xwxq484rlb7c641u5dqu1tbzor"}
	if *events[0] != gotId || *events[1] != waiting {
		t.Fatal(events)
	}
}

func TestUnitReadConnected(t *testing.T) {
	connected_raw := []byte(`[["connected"], ["statusInfo", {"count": 36770, "antinudeservers": ["waw2.omegle.com", "waw3.omegle.com", "waw1.omegle.com"], "spyQueueTime": 9.9992752075199996e-05, "antinudepercent": 1.0, "spyeeQueueTime": 1.6791000127780003, "timestamp": 1371762232.121762, "servers": ["front8.omegle.com", "front7.omegle.com", "front3.omegle.com", "front5.omegle.com", "front9.omegle.com", "front1.omegle.com", "front6.omegle.com", "front2.omegle.com", "front4.omegle.com"]}]]`)
	events, err := parseResponse(connected_raw)
	if err != nil {
		t.Fatal(err)
	}
	connected := Event{Kind: "connected"}

	if *events[0] != connected {
		t.Fatal(events)
	}
}
*/

// TestConversation* are integration tests
func TestConversationEarlyDisconnect(t *testing.T) {
	fmt.Println("testing early disconnect:")
	s := NewSession()

	events := make(chan *Event, 16)

	go func() {
		for event := range events {
			fmt.Println(event)
			if event.Kind == "connected" {
				fmt.Println("requesting disconnect...")
				s.Disconnect()
			}
		}
	}()

	err := s.Connect(events)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Println("--\n")
}

func TestConversation(t *testing.T) {
	fmt.Println("testing read-write conversation:")
	s := NewSession()
	events := make(chan *Event, 16)

	go func() {
		for event := range events {
			fmt.Println(event)
			if event.Kind == "connected" {
				s.Say("can you read this? Omegle has been acting up for me.")
				time.AfterFunc(30*time.Second, func() {
					fmt.Println("<timing out conversation>")
					s.Disconnect()
				})
			}
		}
	}()

	err := s.Connect(events)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Println("--\n")
}
