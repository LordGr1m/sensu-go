// Package agent is the running Sensu agent. Agents connect to a Sensu backend,
// register their presence, subscribe to check channels, download relevant
// check packages, execute checks, and send results to the Sensu backend via
// the Event channel.
package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/sensu/sensu-go/handler"
	"github.com/sensu/sensu-go/transport"
	"github.com/sensu/sensu-go/types"
)

const (
	// MaxMessageBufferSize specifies the maximum number of messages of a given
	// type that an agent will queue before rejecting messages.
	MaxMessageBufferSize = 10
)

// A Config specifies Agent configuration.
type Config struct {
	// BackendURL is the URL to the Sensu Backend.
	BackendURL    string
	Subscriptions []string
}

// An Agent receives and acts on messages from a Sensu Backend.
type Agent struct {
	config       *Config
	backendURL   string
	handler      *handler.MessageHandler
	conn         *transport.Transport
	sendq        chan *message
	disconnected bool
	stopChan     chan struct{}
}

type message struct {
	Type    string
	Payload []byte
}

// NewAgent creates a new Agent and returns a pointer to it.
func NewAgent(config *Config) *Agent {
	return &Agent{
		config:       config,
		backendURL:   config.BackendURL,
		handler:      handler.NewMessageHandler(),
		disconnected: true,
		stopChan:     make(chan struct{}),
		sendq:        make(chan *message, 10),
	}
}

func (a *Agent) receivePump(wg *sync.WaitGroup, conn *transport.Transport) {
	log.Println("connected - starting receivePump")
	for {
		if a.disconnected {
			log.Println("disconnected - stopping receivePump")
			wg.Done()
			return
		}

		t, m, err := conn.Receive()
		if err != nil {
			switch err := err.(type) {
			case transport.ConnectionError:
				a.disconnected = true
			case transport.ClosedError:
				a.disconnected = true
			default:
				log.Println("recv error:", err.Error())
			}
			continue
		}
		log.Println("message received - type: ", t, " message: ", m)

		err = a.handler.Handle(t, m)
		if err != nil {
			log.Println("error handling message:", err.Error())
		}
	}
}

func (a *Agent) sendPump(wg *sync.WaitGroup, conn *transport.Transport) {
	log.Println("connected - starting sendPump")
	ticker := time.NewTicker(100 * time.Millisecond)
	for {
		select {
		case msg := <-a.sendq:
			err := conn.Send(msg.Type, msg.Payload)
			if err != nil {
				switch err := err.(type) {
				case transport.ConnectionError:
					a.disconnected = true
				case transport.ClosedError:
					a.disconnected = true
				default:
					log.Println("recv error:", err.Error())
				}
			}
		case <-ticker.C:
			if a.disconnected {
				log.Println("disconnected - stopping sendPump")
				ticker.Stop()
				wg.Done()
				return
			}
		}
	}
}

func (a *Agent) handshake() error {
	handshake := &types.AgentHandshake{
		Subscriptions: a.config.Subscriptions,
	}
	msgBytes, err := json.Marshal(handshake)
	if err != nil {
		return err
	}

	// shoot first, ask questions later.
	err = a.conn.Send(types.AgentHandshakeType, msgBytes)
	if err != nil {
		return err
	}

	t, m, err := a.conn.Receive()
	if err != nil {
		return err
	}

	if t != types.BackendHandshakeType {
		return errors.New("backend did not send handshake")
	}

	response := types.BackendHandshake{}
	err = json.Unmarshal(m, &response)
	if err != nil {
		return fmt.Errorf("error unmarshaling backend handshake: %s", err.Error())
	}

	return nil
}

// Run starts the Agent's connection manager which handles connecting and
// reconnecting to the Sensu Backend. It also handles coordination of the
// agent's read and write pumps.
//
// If Run cannot establish an initial connection to the specified Backend
// URL, Run will return an error.
func (a *Agent) Run() error {
	conn, err := transport.Connect(a.config.BackendURL)
	if err != nil {
		return err
	}
	a.conn = conn
	wg := &sync.WaitGroup{}
	wg.Add(2)
	err = a.handshake()
	if err != nil {
		return err
	}

	a.disconnected = false
	go a.sendPump(wg, conn)
	go a.receivePump(wg, conn)

	go func(wg *sync.WaitGroup) {
		retries := 0
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-a.stopChan:
				a.conn.Close()
				wg.Wait()
				a.stopChan <- struct{}{}
			case <-ticker.C:
				if a.disconnected {
					log.Println("disconnected - attempting to reconnect: ", a.backendURL)
					wg.Wait()
					conn, err := transport.Connect(a.backendURL)
					if err != nil {
						log.Println("connection error:", err.Error())
						// TODO(greg): exponential backoff
						time.Sleep(1 * time.Second)
						retries++
						// TODO(greg): Figure out a max backoff / max retries thing
						// before we fail over to the configured backend url
						if retries >= 30 {
							a.backendURL = a.config.BackendURL
						}
						continue
					}
					a.conn = conn
					log.Println("reconnected: ", a.backendURL)
					wg.Add(2)
					err = a.handshake()
					if err != nil {
						log.Println("handshake error: ", err.Error())
						continue
					}
					go a.sendPump(wg, conn)
					go a.receivePump(wg, conn)
					a.disconnected = false
				}
			}
		}
	}(wg)

	return nil
}

// Stop will cause the Agent to finish processing requests and then cleanly
// shutdown.
func (a *Agent) Stop() {
	a.stopChan <- struct{}{}
	select {
	case <-a.stopChan:
		return
	case <-time.After(1 * time.Second):
		return
	}
}

func (a *Agent) sendMessage(msgType string, payload []byte) {
	// blocks until message can be enqueued.
	// TODO(greg): ring buffer?
	a.sendq <- &message{msgType, payload}
}

func (a *Agent) addHandler(msgType string, handlerFunc handler.MessageHandlerFunc) {
	a.handler.AddHandler(msgType, handlerFunc)
}
