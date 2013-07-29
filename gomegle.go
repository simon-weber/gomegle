package gomegle

import (
	"errors"
	"fmt"
	"io/ioutil"
	"math/rand"
    "os"
	"net/http"
	"net/url"
	"strings"
	"time"
    "log"
	"github.com/bitly/go-simplejson"
)

func init() {
	rand.Seed(time.Now().UTC().UnixNano())
}

const server = "http://front2.omegle.com/"
const userAgent = "Mozilla/5.0 (Windows NT 5.1) AppleWebKit/537.17 (KHTML, like Gecko) Chrome/24.0.1312.56 Safari/537.17"

/*
Events are tuples sent on a client-provided channel after calling Connect.

To provide a single ordering of events, both serverside events
(eg, stranger sent us a message) and clientside actions
(eg, we sent a message) are sent over the same channel.

Possible events are described below. Events that provide a value
are written as Kind<Value description>:

waiting: server hasn't found a stranger for us yet
connected: we have a stranger
strangerDisconnected
weDisconnected

strangerTyping
strangerStoppedTyping
gotMessage<message text>: stranger sent us a message
gotServerMessage<message text>: server sent us a message (normally to say that the stranger is on a mobile device)

weMessage<message text>
weTyping
weStoppedTyping
*/
type Event struct {
	Kind  string //TODO make this an enum?
	Value string
}

func (e *Event) String() string {
	return fmt.Sprintf("{Event <%s> %v}", e.Kind, e.Value)
}

// A Session can be connected to channel events from a single conversation.
type Session struct {
	ClientId string // identifies us as in a conversation
	SayWPM   int    // words per minute to fake type at for Say

	httpClient     *http.Client
	internalEvents chan []*Event // internal stacked channel of all events
    logger *log.Logger
}

// Create a new default session that types at 160 WPM.
func NewSession() *Session {
    return &Session{httpClient: &http.Client{}, SayWPM: 160, logger: log.New(os.Stderr, "", log.LstdFlags)}
}

func NewCustomSession(sayWPM int, logger *log.Logger) *Session {
    return &Session{httpClient: &http.Client{}, SayWPM: sayWPM, logger: logger}
}

// Connect begins a conversation. Events are sent over eventsChan.
//
// When the conversation is over --
// due to a remote disconnect, local disconnect, or error -- Connect will
// close eventsChan and return.
//
// After returning, Connect can be called again to start a new conversation.
func (s *Session) Connect(eventsChan chan *Event) error {
	// reset conversation state
	s.internalEvents = make(chan []*Event, 32)
	connected := false

	// reactor <-> poller communication
	startPolling := make(chan bool)   // poller <- reactor
	stopPolling := make(chan bool)    // poller <- reactor
	stoppedPolling := make(chan bool) // reactor <- poller

	go func() {
		// continually long-poll omegle for server events and queue them.
		// omegle server has a timeout built-in, no need to have one here
		defer func() {
			stoppedPolling <- true
		}()

		pollChan := make(chan []*Event, 32)
		<-startPolling

		for {
			go func() {
				events, err := s.pollForStrangerEvents() // blocking
				if err != nil {
                    s.logger.Printf("error while polling: %s\n", err)
                    // this goroutine and the select below run in lockstep
                    // we need to signal that it's ok to proceed and try to poll again
                    pollChan <- nil
				} else {
					pollChan <- events
				}
				time.Sleep(1 * time.Second)
			}()

			select {
			case <-stopPolling:
				return
			case events := <-pollChan:
                if events != nil{
                    s.queueEvents(events)
                }
			}
		}
	}()

	s.internalEvents <- []*Event{&Event{Kind: "doConnect"}}

	defer func() {
		stopPolling <- true
		<-stoppedPolling
		close(eventsChan)
	}()
	for {
		events := <-s.internalEvents
		for _, event := range events {
			switch event.Kind {
			case "doConnect":
				if connected {
					return errors.New("attempted to Connect when already connected")
				}
				events, err := s.doConnect()
				if err != nil {
					return fmt.Errorf("error during doConnect: %s", err)
				}
				s.queueEvents(events)

			case "doDisconnect":
				if !connected {
					return errors.New("attempted to Disconnect when not connected")
				}
				if _, err := s.doDisconnect(); err != nil {
					return fmt.Errorf("error during doDisconnect: %s", err)
				}
				connected = false
				eventsChan <- &Event{Kind: "weDisconnected", Value: ""}
				return nil

			case "strangerDisconnected":
				_, err := s.doDisconnect()
				if err != nil {
					return fmt.Errorf("error disconnecting from stranger: %s", err)
				}
				connected = false
				eventsChan <- event
				return nil

			case "doSay":
				if !connected {
					return errors.New("attempted to Say when not connected")
				}
				_, err := s.doTyping()
				eventsChan <- &Event{Kind: "weTyping"}

				time.AfterFunc(s.fakeTypingPause(event.Value), func() {
                    defer func() {
                        // there's a race here between sending the event and 
                        // closing the channel, so we might have to recover.

                        // I figure it's better to be optimistic than make this
                        // a blocking sleep.
                        if r := recover(); r != nil {
                            //fmt.Println("recovered from fake typing pause race", r)
                        }
                    }()

					_, err = s.doMessage(event.Value)
					if err != nil {
                        s.logger.Printf("error during doMessage: %s\n", err)
					} else {
                        eventsChan <- &Event{Kind: "weMessage", Value: event.Value}
                    }
				})

			case "doMessage":
				if !connected {
					return errors.New("attempted to Message when not connected")
				}
				_, err := s.doMessage(event.Value)
				if err != nil {
					return fmt.Errorf("error during doMessage: %s", err)
				}
				eventsChan <- &Event{Kind: "weMessage", Value: event.Value}

			case "doTyping":
				if !connected {
					return errors.New("attempted Typing when not connected")
				}
				_, err := s.doTyping()
				if err != nil {
					return fmt.Errorf("error during doTyping: %s", err)
				}
				eventsChan <- &Event{Kind: "weTyping"}
			case "doStoppedTyping":
				if !connected {
					return errors.New("attempted StoppedTyping when not connected")
				}
				_, err := s.doStoppedTyping()
				if err != nil {
					return fmt.Errorf("error during doStoppedTyping: %s", err)
				}
				eventsChan <- &Event{Kind: "weStoppedTyping"}
			case "gotId":
                //special internal event; not sent along
				s.ClientId = event.Value
			case "waiting":
				startPolling <- true
				eventsChan <- event
			case "connected":
				connected = true
				eventsChan <- event
			default:
				eventsChan <- event
			}
		}
	}
}

// Disconnect from the current conversation.
// Can be called at any time while connected.
func (s *Session) Disconnect() {
	s.queueEvents([]*Event{&Event{Kind: "doDisconnect", Value: ""}})
}

// Say makes it appear as if a human typed the message by sending typing events and pausing.
func (s *Session) Say(msg string) {
	s.queueEvents([]*Event{&Event{Kind: "doSay", Value: msg}})
}

// Unlike Say, Message does not pause or send typing events, just the message.
// It's recommended to use Say instead -- this may cause your messages to be
// flagged as abuse.
func (s *Session) Message(msg string) {
	s.queueEvents([]*Event{&Event{Kind: "doMessage", Value: msg}})
}

func (s *Session) Typing() {
	s.queueEvents([]*Event{&Event{Kind: "doTyping"}})
}

func (s *Session) StopTyping() {
	s.queueEvents([]*Event{&Event{Kind: "doStopTyping"}})
}

func (s *Session) queueEvents(events []*Event) {
    /*
    This post:
    http://gowithconfidence.tumblr.com/post/31426832143/stacked-channels
    refers to this pattern as "stacking channels".
    Basically, Go has no infinitely buffered channels, so when we want to append,
    we can pull off a current element, combine it with the new one, then add it back.
    With a fair scheduler, this is guaranteed to make progress.
    */

	if len(events) == 0 {
		return
	}
	for {
		select {
		case s.internalEvents <- events:
			return
		case queuedEvents := <-s.internalEvents:
			events = append(queuedEvents, events...)
		}
	}
}

// These methods are blocking.
func (s *Session) doConnect() ([]*Event, error) {
	args := url.Values{}
	args.Set("rcs", "1")
	args.Set("firstevents", "1")
	args.Set("spid", "")
	args.Set("randid", randId())

	body := url.Values{}

	rawResp, err := s.rawRequest("start", args, body)
	if err != nil {
		return nil, err
	}

	events, err := s.parseResponse(rawResp)
	if err != nil {
		return nil, err
	}

	return events, nil
}

func (s *Session) doDisconnect() ([]*Event, error) {
	args := url.Values{}

	body := url.Values{}
	body.Set("id", s.ClientId)

	rawResp, err := s.rawRequest("disconnect", args, body)
	if err != nil {
		return nil, err
	}

	events, err := s.parseResponse(rawResp)
	if err != nil {
		return nil, err
	}

	return events, nil
}

// pollForStrangerEvents sends a blocking long-poll request for actions the stranger has performed,
// then parses and returns the results.
func (s *Session) pollForStrangerEvents() ([]*Event, error) {
	args := url.Values{}

	body := url.Values{}
	body.Set("id", s.ClientId)

	rawResp, err := s.rawRequest("events", args, body)
	if err != nil {
        return nil, fmt.Errorf("error in request: %s", err)
	}

	events, err := s.parseResponse(rawResp)
	if err != nil {
        return nil, fmt.Errorf("error parsing poll response: %s", err)
	}

	return events, nil
}

func (s *Session) doMessage(message string) ([]*Event, error) {
	args := url.Values{}

	body := url.Values{}
	body.Set("id", s.ClientId)
	body.Set("msg", message)

	rawResp, err := s.rawRequest("send", args, body)
	if err != nil {
		return nil, err
	}

	events, err := s.parseResponse(rawResp)
	if err != nil {
		return nil, err
	}

	return events, nil
}

func (s *Session) doTyping() ([]*Event, error) {
	args := url.Values{}

	body := url.Values{}
	body.Set("id", s.ClientId)

	rawResp, err := s.rawRequest("typing", args, body)
	if err != nil {
		return nil, err
	}

	events, err := s.parseResponse(rawResp)
	if err != nil {
		return nil, err
	}

	return events, nil
}

func (s *Session) doStoppedTyping() ([]*Event, error) {
	args := url.Values{}

	body := url.Values{}
	body.Set("id", s.ClientId)

	rawResp, err := s.rawRequest("stoppedtyping", args, body)
	if err != nil {
		return nil, err
	}

	events, err := s.parseResponse(rawResp)
	if err != nil {
		return nil, err
	}

	return events, nil
}

func (s *Session) parseResponse(body []byte) ([]*Event, error) {
    // omegle's return schemas vary by endpoint and response,
    // which makes them a real pain to deal with in Go.

	events := []*Event{}

	if string(body) == "null" || string(body) == "win" {
		return events, nil // no events/success
	}

	data, err := simplejson.NewJson(body)
	if err != nil {
		return nil, err
	}

	eventsJson, ok := data.CheckGet("events")
	if ok {
		// it's a start schema, with a toplevel object
		clientId, err := data.Get("clientID").String()
		if err != nil {
			return nil, errors.New("could not extract clientID field")
		}
		events = append(events, &Event{Kind: "gotId", Value: clientId})
	} else {
		// it's a toplevel array
		eventsJson = data
	}

	eventsAr, err := eventsJson.Array()
	if err != nil {
		return nil, fmt.Errorf("events json was not an array: %s", eventsJson)
	}

	for i := range eventsAr {
		event := eventsJson.GetIndex(i)
		value := ""
		kind, err := event.GetIndex(0).String()
		if err != nil {
            s.logger.Printf("could not coerce event[0] to string: %v\n", event)
		}

		switch kind {
		case "waiting", "connected", "strangerDisconnected":
		case "typing", "stoppedTyping":
			kind = "stranger" + strings.ToUpper(kind[0:1]) + kind[1:]
		case "gotMessage":
			value = event.GetIndex(1).MustString()
		case "serverMessage":
			kind = "gotServerMessage"
			value = event.GetIndex(1).MustString()
		case "statusInfo":
			// if status ends up being needed
			//response.Status = event.GetIndex(1)
		default:
            s.logger.Printf("unknown event format: %s\n", event) 
		}

		if kind != "statusInfo" {
			events = append(events, &Event{kind, value})
		}
	}

	return events, nil
}

//rawRequest sends a blocking request to Omegle, and returns the response body.
func (s *Session) rawRequest(path string, args url.Values, body url.Values) ([]byte, error) {
	bodyReader := strings.NewReader(body.Encode())
	req, err := http.NewRequest("POST", server+path+"?"+args.Encode(), bodyReader)
	if err != nil {
        return nil, fmt.Errorf("rawRequest: malformed request (%s)", err)
	}

	req.Header.Add("User-Agent", userAgent)
	req.Header.Add("Content-Type", "application/x-www-form-urlencoded")

	resp, err := s.httpClient.Do(req)
	if err != nil {
        return nil, fmt.Errorf("rawRequest: Do error (%s)", err)
	}

	defer resp.Body.Close()
	respBody, err := ioutil.ReadAll(resp.Body)
	if err != nil {
        return nil, fmt.Errorf("rawRequest: ReadAll error (%s)", err)
	}

	return respBody, nil
}

func (s *Session) fakeTypingPause(msg string) time.Duration {
	words := len(msg)/5 + 1
	return time.Duration(1000*60/s.SayWPM*words) * time.Millisecond
}

//Return a 8 digit random string of digits and capital letters.
func randId() string {
	bytes := make([]byte, 8)
	for i := 0; i < 8; i++ {
		//r is the desired ascii value
		r := 65 + rand.Intn(90+10-65)

		if r > 90 {
			//map onto a digit
			r -= 43
		}

		bytes[i] = byte(r)
	}

	return string(bytes)
}
