package harness

import (
	"context"
	"fmt"
	"sync"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
)

// MQTT publishes into the broker the service consumes from, and can watch
// what the service itself publishes.
//
// Publishing before the service has subscribed loses the message silently —
// MQTT has no offsets to fall back on — so always wait for the service's
// subscription log line before the first publish of a scenario.
type MQTT struct {
	Broker string
	client paho.Client

	mu       sync.Mutex
	received map[string][]Message
}

// Message is one delivery the suite observed.
type Message struct {
	Topic    string
	Payload  []byte
	Retained bool
	At       time.Time
}

// ConnectMQTT dials the broker as the suite's own client.
func ConnectMQTT(broker, clientID string, timeout time.Duration) (*MQTT, error) {
	opts := paho.NewClientOptions().
		AddBroker(broker).
		SetClientID(clientID).
		SetConnectTimeout(timeout).
		SetAutoReconnect(true).
		SetCleanSession(true)

	m := &MQTT{Broker: broker, received: map[string][]Message{}}
	m.client = paho.NewClient(opts)
	token := m.client.Connect()
	if !token.WaitTimeout(timeout) {
		return nil, fmt.Errorf("connecting to mqtt broker %s timed out after %s", broker, timeout)
	}
	if err := token.Error(); err != nil {
		return nil, fmt.Errorf("connecting to mqtt broker %s: %w", broker, err)
	}
	return m, nil
}

// Close disconnects.
func (m *MQTT) Close() { m.client.Disconnect(250) }

// Publish sends a payload. body may be []byte, string, or any JSON-marshalable
// value. Passing raw bytes is how malformed-input scenarios send bytes the
// service's decoder cannot parse.
func (m *MQTT) Publish(topic string, qos byte, retained bool, body any) error {
	payload, err := encodePayload(body)
	if err != nil {
		return err
	}
	token := m.client.Publish(topic, qos, retained, payload)
	if !token.WaitTimeout(5 * time.Second) {
		return fmt.Errorf("publishing to %s timed out", topic)
	}
	return token.Error()
}

// PublishMarked adds the receipt marker naming the scenario that sent it, so
// each scenario has one unambiguous log line to wait for even when two
// scenarios publish otherwise identical messages. Check the contract says
// unknown fields are ignored before relying on it.
func (m *MQTT) PublishMarked(topic string, qos byte, retained bool, event map[string]any, scenario string) error {
	marked := make(map[string]any, len(event)+1)
	for k, v := range event {
		marked[k] = v
	}
	marked[MarkerField] = scenario
	return m.Publish(topic, qos, retained, marked)
}

// ClearRetained removes a retained message, so the next run does not inherit
// this one's replay fixture.
func (m *MQTT) ClearRetained(topic string) error {
	return m.Publish(topic, 1, true, []byte{})
}

// Watch subscribes so the suite can assert on what the service publishes —
// its own outbound topics, or a last will.
func (m *MQTT) Watch(topic string, qos byte) error {
	token := m.client.Subscribe(topic, qos, func(_ paho.Client, msg paho.Message) {
		m.mu.Lock()
		defer m.mu.Unlock()
		m.received[topic] = append(m.received[topic], Message{
			Topic: msg.Topic(), Payload: msg.Payload(), Retained: msg.Retained(), At: time.Now(),
		})
	})
	if !token.WaitTimeout(5 * time.Second) {
		return fmt.Errorf("subscribing to %s timed out", topic)
	}
	return token.Error()
}

// WaitForMessage waits for a message on a watched topic satisfying match.
func (m *MQTT) WaitForMessage(ctx context.Context, topic string, timeout time.Duration, match func(Message) bool) (Message, error) {
	var found Message
	err := Until(ctx, timeout, "an mqtt message on "+topic, func(context.Context) (bool, error) {
		m.mu.Lock()
		defer m.mu.Unlock()
		for _, msg := range m.received[topic] {
			if match == nil || match(msg) {
				found = msg
				return true, nil
			}
		}
		return false, nil
	})
	return found, err
}

// Drain forgets what has been received, so one scenario cannot match an
// earlier scenario's message.
func (m *MQTT) Drain() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.received = map[string][]Message{}
}
